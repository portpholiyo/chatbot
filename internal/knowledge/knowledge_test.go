package knowledge

import (
	"regexp"
	"strings"
	"testing"
)

func TestNoPrivateContactDetails(t *testing.T) {
	txt := Text()
	if regexp.MustCompile(`\+?\d[\d\s-]{9,}`).MatchString(txt) {
		t.Error("knowledge base appears to contain a phone number")
	}
	for _, e := range regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`).FindAllString(txt, -1) {
		if e != "contact@example.com" {
			t.Errorf("unexpected email address in knowledge base: %s", e)
		}
	}
}

func TestMissingMarkersAreWellFormed(t *testing.T) {
	if n := strings.Count(raw, "[[MISSING"); n != len(MissingItems()) {
		t.Errorf("%d MISSING markers but only %d parse correctly", n, len(MissingItems()))
	}
}
