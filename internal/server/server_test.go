package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpholiyo/chatbot/internal/llm"
	"github.com/portpholiyo/chatbot/internal/store"
)

type fakeStore struct {
	mu    sync.Mutex
	msgs  []store.Message
	count int
}

func (f *fakeStore) Insert(_ context.Context, m *store.Message) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, *m)
	return int64(len(f.msgs)), nil
}
func (f *fakeStore) History(context.Context, string, int) ([]store.Turn, error) { return nil, nil }
func (f *fakeStore) CountToday(context.Context) (int, error)                    { return f.count, nil }
func (f *fakeStore) Ping(context.Context) error                                 { return nil }
func (f *fakeStore) List(context.Context, store.ListFilter) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Message(nil), f.msgs...), nil
}

type fakeLLM struct {
	allow     bool
	guardErr  error
	chunks    []string
	streamErr error
	streamed  bool
}

func (f *fakeLLM) Allowed(context.Context, []llm.Turn, string) (bool, llm.Usage, error) {
	return f.allow, llm.Usage{InputTokens: 10, OutputTokens: 1}, f.guardErr
}
func (f *fakeLLM) Stream(_ context.Context, _ []llm.Turn, _ string, on func(string) error) (llm.Usage, error) {
	f.streamed = true
	for _, c := range f.chunks {
		if err := on(c); err != nil {
			return llm.Usage{}, err
		}
	}
	return llm.Usage{InputTokens: 100, OutputTokens: 20}, f.streamErr
}

const sid = "123e4567-e89b-42d3-a456-426614174000"

func newTestServer(t *testing.T, st *fakeStore, l *fakeLLM) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return New(ctx, Options{
		Store: st, LLM: l, Model: "test-model", AdminToken: strings.Repeat("a", 40), IPSalt: "salt",
		AllowedOrigins: []string{"https://example.com"}, DailyCap: 200, MaxMessageChars: 100,
		HistoryTurns: 10, MaxConcurrent: 2, IPRatePerMin: 60, IPBurst: 3, RequestTimeout: 5 * time.Second,
	})
}

func post(s *Server, body any, origin string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(b))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.RemoteAddr = "203.0.113.9:1234"
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestChatAnswered(t *testing.T) {
	st, l := &fakeStore{}, &fakeLLM{allow: true, chunks: []string{"[G", "AP] I don't", " know."}}
	rr := post(newTestServer(t, st, l), map[string]string{"session_id": sid, "message": "hobbies?"}, "https://example.com")
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "GAP") {
		t.Fatalf("marker leaked: %s", rr.Body)
	}
	if len(st.msgs) != 1 {
		t.Fatalf("want 1 saved message, got %d", len(st.msgs))
	}
	m := st.msgs[0]
	if m.Status != store.StatusAnswered || !m.KnowledgeGap || m.Answer != "I don't know." || m.InputTokens != 110 {
		t.Fatalf("unexpected record: %+v", m)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatal("missing CORS header")
	}
}

func TestChatRefusedSkipsAnswerModel(t *testing.T) {
	st, l := &fakeStore{}, &fakeLLM{allow: false}
	rr := post(newTestServer(t, st, l), map[string]string{"session_id": sid, "message": "write me a poem"}, "")
	if rr.Code != 200 || l.streamed {
		t.Fatalf("code=%d streamed=%v", rr.Code, l.streamed)
	}
	if st.msgs[0].Status != store.StatusRefused || st.msgs[0].Answer != llm.RefusalText {
		t.Fatalf("unexpected record: %+v", st.msgs[0])
	}
}

func TestGuardErrorFailsClosed(t *testing.T) {
	st, l := &fakeStore{}, &fakeLLM{guardErr: errors.New("boom")}
	rr := post(newTestServer(t, st, l), map[string]string{"session_id": sid, "message": "hi"}, "")
	if rr.Code != http.StatusBadGateway || l.streamed {
		t.Fatalf("code=%d streamed=%v", rr.Code, l.streamed)
	}
	if st.msgs[0].Status != store.StatusError {
		t.Fatalf("unexpected record: %+v", st.msgs[0])
	}
}

func TestValidation(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, &fakeLLM{allow: true})
	cases := map[string]map[string]string{
		"bad session": {"session_id": "nope", "message": "hi"},
		"empty":       {"session_id": sid, "message": "   "},
		"too long":    {"session_id": sid, "message": strings.Repeat("x", 101)},
	}
	for name, body := range cases {
		if rr := post(s, body, ""); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, rr.Code)
		}
	}
}

func TestOriginBlocked(t *testing.T) {
	rr := post(newTestServer(t, &fakeStore{}, &fakeLLM{allow: true}), map[string]string{"session_id": sid, "message": "hi"}, "https://evil.example")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDailyCap(t *testing.T) {
	s := newTestServer(t, &fakeStore{count: 200}, &fakeLLM{allow: true})
	if rr := post(s, map[string]string{"session_id": sid, "message": "hi"}, ""); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestRateLimit(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, &fakeLLM{allow: true, chunks: []string{"ok"}})
	var last int
	for i := 0; i < 5; i++ {
		last = post(s, map[string]string{"session_id": sid, "message": "hi"}, "").Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst, got %d", last)
	}
}

func TestAdminAuth(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, &fakeLLM{})
	for token, want := range map[string]int{"": 401, "wrong": 401, strings.Repeat("a", 40): 200} {
		req := httptest.NewRequest(http.MethodGet, "/admin/messages", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("token %q: got %d want %d", token, rr.Code, want)
		}
	}
}

func TestGapFilter(t *testing.T) {
	cases := []struct {
		chunks []string
		out    string
		gap    bool
	}{
		{[]string{"[GAP] hello"}, "hello", true},
		{[]string{"[G", "AP]", " hi"}, "hi", true},
		{[]string{"Hello ", "[GAP] world"}, "Hello [GAP] world", false},
		{[]string{"[GA"}, "[GA", false},
		{[]string{"[X"}, "[X", false},
	}
	for _, c := range cases {
		var f gapFilter
		var out string
		for _, ch := range c.chunks {
			out += f.Push(ch)
		}
		out += f.Flush()
		if out != c.out || f.Gap() != c.gap {
			t.Errorf("%v: got %q gap=%v", c.chunks, out, f.Gap())
		}
	}
}

func TestSessionID(t *testing.T) {
	if !sessionRe.MatchString(NewSessionID()) {
		t.Fatal("generated id does not match pattern")
	}
}
