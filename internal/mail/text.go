package mail

import (
	"strings"
	"unicode"
)

// NormalizeBodyForRender turns CRLF and CR into LF and removes the line
// breaks at the end. All other whitespace stays as it is.
func NormalizeBodyForRender(body string) string {
	return strings.TrimRight(normalizeLineEndings(body), "\n")
}

// SubjectSlug turns a subject into the readable part of a file name. The
// result is lower case, with hyphens between words. Letters and digits from
// every script stay. It is at most 80 bytes, and an empty result becomes
// "no-subject".
func SubjectSlug(subject string) string {
	var b strings.Builder
	prevSep := false
	for _, r := range strings.ToLower(toNFC(subject)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
			prevSep = false
			continue
		}
		if !prevSep {
			b.WriteByte('-')
			prevSep = true
		}
	}
	slug := strings.TrimRight(truncateBytes(strings.Trim(b.String(), "-"), 80), "-")
	if slug == "" {
		return "no-subject"
	}
	return slug
}
