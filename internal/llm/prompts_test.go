package llm

import (
	"strings"
	"testing"
)

func TestAnswerPromptEmbedsKnowledgeAndRules(t *testing.T) {
	p := AnswerPrompt("FACT-123")
	for _, want := range []string{"FACT-123", GapMarker, "contact@example.com", "ONLY"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestGuardInputKeepsOnlyRecentTurns(t *testing.T) {
	h := []Turn{{"q1", "a1"}, {"q2", "a2"}, {"q3", "a3"}}
	in := GuardInput(h, "latest")
	if strings.Contains(in, "q1") || !strings.Contains(in, "q3") || !strings.Contains(in, "<visitor_message>\nlatest") {
		t.Fatalf("unexpected guard input: %s", in)
	}
}
