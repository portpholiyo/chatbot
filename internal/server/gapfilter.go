package server

import (
	"strings"

	"github.com/portpholiyo/chatbot/internal/llm"
)

// gapFilter removes the [GAP] marker from the start of a streamed answer and
// remembers that it was present. Text is held back only while it could still
// turn out to be the marker.
type gapFilter struct {
	buf     strings.Builder
	decided bool
	gap     bool
	trim    bool // drop leading whitespace after the marker
}

// Push takes the next chunk and returns the text that is safe to send.
func (g *gapFilter) Push(chunk string) string {
	if g.decided {
		if g.trim {
			chunk = strings.TrimLeft(chunk, " \n\t")
			g.trim = chunk == ""
		}
		return chunk
	}
	g.buf.WriteString(chunk)
	trimmed := strings.TrimLeft(g.buf.String(), " \n\t")
	if len(trimmed) < len(llm.GapMarker) && strings.HasPrefix(llm.GapMarker, trimmed) {
		return "" // still ambiguous, keep buffering
	}
	g.decided = true
	if strings.HasPrefix(trimmed, llm.GapMarker) {
		g.gap = true
		rest := strings.TrimLeft(trimmed[len(llm.GapMarker):], " \n\t")
		g.trim = rest == ""
		return rest
	}
	return g.buf.String()
}

// Flush returns any text still buffered when the stream ends.
func (g *gapFilter) Flush() string {
	if g.decided {
		return ""
	}
	g.decided = true
	return g.buf.String()
}

// Gap reports whether the marker was seen.
func (g *gapFilter) Gap() bool { return g.gap }
