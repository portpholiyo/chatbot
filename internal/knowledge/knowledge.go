// Package knowledge embeds the facts the chatbot is allowed to talk about.
package knowledge

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed knowledge.md
var raw string

// Text returns the knowledge base used in the system prompt.
func Text() string { return strings.TrimSpace(raw) }

var missingRe = regexp.MustCompile(`\[\[MISSING:[^\]]*\]\]`)

// MissingItems lists every [[MISSING: ...]] marker still present in the
// knowledge base, so gaps are easy to find and fill.
func MissingItems() []string { return missingRe.FindAllString(raw, -1) }
