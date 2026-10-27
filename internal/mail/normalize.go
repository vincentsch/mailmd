package mail

import (
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// isASCIISpace is the whitespace test of all helpers here. It ignores
// non-ASCII spaces, so a new Unicode version can't change file names or IDs.
func isASCIISpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

func toNFC(s string) string {
	return norm.NFC.String(s)
}

func trimASCIISpace(s string) string {
	return strings.TrimFunc(s, isASCIISpace)
}

func collapseASCIISpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, isASCIISpace), " ")
}

func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// stripOneAnglePair removes one pair of angle brackets around s. Any other
// brackets stay, because they are part of the value.
func stripOneAnglePair(s string) string {
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s[1 : len(s)-1]
	}
	return s
}

// sortedUnique returns a sorted copy without duplicates. It doesn't change
// the slice it gets.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := append([]string(nil), in...)
	sort.Strings(out)

	n := 0
	for _, s := range out {
		if n == 0 || out[n-1] != s {
			out[n] = s
			n++
		}
	}
	return out[:n]
}

func isLowerHex16(s string) bool {
	if len(s) < 16 {
		return false
	}
	for i := 0; i < 16; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
