package mail

import (
	"reflect"
	"testing"
)

func TestNormalizeHelpers(t *testing.T) {
	if got := collapseASCIISpace(" \talpha\n\nbeta\vgamma\f "); got != "alpha beta gamma" {
		t.Fatalf("collapseASCIISpace() = %q", got)
	}
	if got := collapseASCIISpace("a\u00a0b"); got != "a\u00a0b" {
		t.Fatalf("collapseASCIISpace collapsed non-ASCII space: %q", got)
	}
	if got := trimASCIISpace("\t value \r\n"); got != "value" {
		t.Fatalf("trimASCIISpace() = %q", got)
	}
	if got := trimASCIISpace("\u00a0value\u00a0"); got != "\u00a0value\u00a0" {
		t.Fatalf("trimASCIISpace trimmed non-ASCII space: %q", got)
	}
	if got := normalizeLineEndings("a\r\nb\rc\n"); got != "a\nb\nc\n" {
		t.Fatalf("normalizeLineEndings() = %q", got)
	}
	if got := stripOneAnglePair("<abc>"); got != "abc" {
		t.Fatalf("stripOneAnglePair(<abc>) = %q", got)
	}
	if got := stripOneAnglePair("<>"); got != "" {
		t.Fatalf("stripOneAnglePair(<>) = %q", got)
	}
	if got := stripOneAnglePair("<<abc>>"); got != "<abc>" {
		t.Fatalf("stripOneAnglePair(<<abc>>) = %q", got)
	}

	if got := sortedUnique([]string{"b", "a", "b", ""}); !reflect.DeepEqual(got, []string{"", "a", "b"}) {
		t.Fatalf("sortedUnique() = %#v", got)
	}
	if got := sortedUnique(nil); got == nil || len(got) != 0 {
		t.Fatalf("sortedUnique(nil) = %#v", got)
	}

	for _, s := range []string{"0123456789abcdef", "0123456789abcdefz"} {
		if !isLowerHex16(s) {
			t.Fatalf("isLowerHex16(%q) = false", s)
		}
	}
	for _, s := range []string{"0123456789abcde", "0123456789abcdeG", "0123456789ABCDEf"} {
		if isLowerHex16(s) {
			t.Fatalf("isLowerHex16(%q) = true", s)
		}
	}
}
