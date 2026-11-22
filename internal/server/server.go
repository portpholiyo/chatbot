// Package server contains the HTTP API: chat (SSE), health and admin endpoints.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/portpholiyo/chatbot/internal/llm"
	"github.com/portpholiyo/chatbot/internal/store"
)

// Store is the persistence the server needs (implemented by *store.Store).
type Store interface {
	Insert(ctx context.Context, m *store.Message) (int64, error)
	History(ctx context.Context, sessionID string, limit int) ([]store.Turn, error)
	CountToday(ctx context.Context) (int, error)
	List(ctx context.Context, f store.ListFilter) ([]store.Message, error)
	Ping(ctx context.Context) error
}

// Options configures a Server.
type Options struct {
	Store           Store
	LLM             llm.Client
	Model           string
	AdminToken      string
	IPSalt          string
	AllowedOrigins  []string
	ClientIPHeader  string
	DailyCap        int
	MaxMessageChars int
	HistoryTurns    int
	MaxConcurrent   int
	IPRatePerMin    int
	IPBurst         int
	RequestTimeout  time.Duration
}

// Server is the HTTP handler.
type Server struct {
	opts    Options
	limiter *ipLimiter
	sem     chan struct{}
	origins map[string]struct{}
	mux     *http.ServeMux
}

var sessionRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// New builds the server. ctx bounds background goroutines.
func New(ctx context.Context, o Options) *Server {
	s := &Server{
		opts:    o,
		limiter: newIPLimiter(ctx, o.IPRatePerMin, o.IPBurst),
		sem:     make(chan struct{}, o.MaxConcurrent),
		origins: make(map[string]struct{}, len(o.AllowedOrigins)),
		mux:     http.NewServeMux(),
	}
	for _, origin := range o.AllowedOrigins {
		s.origins[origin] = struct{}{}
	}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /api/chat", s.chat)
	if o.AdminToken != "" {
		s.mux.HandleFunc("GET /admin/messages", s.adminMessages)
	}
	return s
}

// Handler returns the full handler chain.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logRequests(s.cors(s.mux)))
}

// ---------- middleware ----------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "panic", rec, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, errBody("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		if origin := r.Header.Get("Origin"); origin != "" {
			if _, ok := s.origins[origin]; ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
			} else if r.Method == http.MethodOptions || r.URL.Path == "/api/chat" {
				writeJSON(w, http.StatusForbidden, errBody("origin not allowed"))
				return
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- handlers ----------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.opts.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("database unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req chatRequest
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if !sessionRe.MatchString(req.SessionID) {
		writeJSON(w, http.StatusBadRequest, errBody("invalid session_id"))
		return
	}
	msg, err := s.cleanMessage(req.Message)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusTooManyRequests, errBody("You're sending messages too quickly. Please wait a few seconds."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.RequestTimeout)
	defer cancel()

	n, err := s.opts.Store.CountToday(ctx)
	if err != nil {
		slog.Error("count today", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errBody("service temporarily unavailable"))
		return
	}
	if n >= s.opts.DailyCap {
		writeJSON(w, http.StatusServiceUnavailable, errBody("The assistant has reached its daily message limit. Please come back tomorrow."))
		return
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, errBody("The assistant is busy. Please try again in a moment."))
		return
	}

	history, err := s.opts.Store.History(ctx, req.SessionID, s.opts.HistoryTurns)
	if err != nil {
		slog.Error("load history", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errBody("service temporarily unavailable"))
		return
	}
	llmHist := make([]llm.Turn, len(history))
	for i, t := range history {
		llmHist[i] = llm.Turn{Question: t.Question, Answer: t.Answer}
	}

	rec := &store.Message{
		SessionID: req.SessionID,
		IPHash:    s.hashIP(ip),
		UserAgent: truncate(r.UserAgent(), 300),
		Question:  msg,
		Model:     s.opts.Model,
	}
	start := time.Now()
	save := func() int64 {
		rec.LatencyMS = int(time.Since(start).Milliseconds())
		// Persist even if the client disconnected mid-stream.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer scancel()
		id, err := s.opts.Store.Insert(sctx, rec)
		if err != nil {
			slog.Error("save message", "err", err)
		}
		return id
	}

	// Layer 1: topic guard.
	allowed, gu, err := s.opts.LLM.Allowed(ctx, llmHist, msg)
	rec.InputTokens, rec.OutputTokens = gu.InputTokens, gu.OutputTokens
	if err != nil {
		slog.Error("guard failed", "err", err)
		rec.Status, rec.Error = store.StatusError, truncate(err.Error(), 500)
		save()
		writeJSON(w, http.StatusBadGateway, errBody("The assistant is unavailable right now. Please try again shortly."))
		return
	}

	sse, ok := newSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errBody("streaming unsupported"))
		return
	}

	if !allowed {
		rec.Status, rec.Answer = store.StatusRefused, llm.RefusalText
		id := save()
		_ = sse.event("delta", map[string]string{"text": llm.RefusalText})
		_ = sse.event("done", map[string]any{"id": id, "refused": true})
		return
	}

	// Layer 2: answer constrained by the system prompt and knowledge base.
	var (
		full   strings.Builder
		filter gapFilter
	)
	emit := func(text string) error {
		if text == "" {
			return nil
		}
		full.WriteString(text)
		return sse.event("delta", map[string]string{"text": text})
	}
	au, err := s.opts.LLM.Stream(ctx, llmHist, msg, func(chunk string) error {
		return emit(filter.Push(chunk))
	})
	rec.InputTokens += au.InputTokens
	rec.OutputTokens += au.OutputTokens
	if err == nil {
		err = emit(filter.Flush())
	}
	rec.Answer = full.String()
	rec.KnowledgeGap = filter.Gap()
	if err != nil {
		slog.Error("answer failed", "err", err)
		rec.Status, rec.Error = store.StatusError, truncate(err.Error(), 500)
		save()
		if !errors.Is(err, context.Canceled) {
			_ = sse.event("error", map[string]string{"message": "The assistant hit a problem. Please try again."})
		}
		return
	}
	rec.Status = store.StatusAnswered
	id := save()
	_ = sse.event("done", map[string]any{"id": id, "refused": false})
}

func (s *Server) adminMessages(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(auth, "Bearer ")
	if !found || subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.AdminToken)) != 1 {
		// Count attempts against the same per-IP limiter to slow guessing.
		s.limiter.Allow("admin:" + s.clientIP(r))
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}
	q := r.URL.Query()
	f := store.ListFilter{Status: q.Get("status"), GapOnly: q.Get("gap") == "1"}
	switch f.Status {
	case "", store.StatusAnswered, store.StatusRefused, store.StatusError:
	default:
		writeJSON(w, http.StatusBadRequest, errBody("invalid status"))
		return
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("before"); v != "" {
		f.Before, _ = strconv.ParseInt(v, 10, 64)
	}
	msgs, err := s.opts.Store.List(r.Context(), f)
	if err != nil {
		slog.Error("admin list", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// ---------- helpers ----------

func (s *Server) cleanMessage(m string) (string, error) {
	if !utf8.ValidString(m) {
		return "", errors.New("message must be valid UTF-8")
	}
	m = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, m)
	m = strings.TrimSpace(m)
	if m == "" {
		return "", errors.New("message is empty")
	}
	if utf8.RuneCountInString(m) > s.opts.MaxMessageChars {
		return "", fmt.Errorf("message is too long (max %d characters)", s.opts.MaxMessageChars)
	}
	return m, nil
}

func (s *Server) clientIP(r *http.Request) string {
	if h := s.opts.ClientIPHeader; h != "" {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) hashIP(ip string) string {
	mac := hmac.New(sha256.New, []byte(s.opts.IPSalt))
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil))[:24]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// NewSessionID returns a random UUIDv4 (handy for tools and tests).
func NewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ---------- server-sent events ----------

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
