package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

var reservedDeviceNames = map[string]struct{}{
	"CON":  {},
	"PRN":  {},
	"AUX":  {},
	"NUL":  {},
	"COM1": {},
	"COM2": {},
	"COM3": {},
	"COM4": {},
	"COM5": {},
	"COM6": {},
	"COM7": {},
	"COM8": {},
	"COM9": {},
	"LPT1": {},
	"LPT2": {},
	"LPT3": {},
	"LPT4": {},
	"LPT5": {},
	"LPT6": {},
	"LPT7": {},
	"LPT8": {},
	"LPT9": {},
}

// SafePathSegment turns a name into a file or folder name that works on
// Linux, macOS and Windows. Letters and digits from every script stay as
// they are. Control characters and the characters Windows forbids, like :
// and ?, become hyphens. The result never starts with a dot and is never a
// Windows device name like CON. It is at most 120 bytes long.
func SafePathSegment(raw string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range toNFC(raw) {
		if r == '-' || !isSafePathRune(r) {
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
			continue
		}
		b.WriteRune(r)
		prevHyphen = false
	}
	segment := strings.Join(strings.Fields(b.String()), " ")
	segment = strings.TrimRight(strings.Trim(segment, " -"), ".")
	if strings.Trim(segment, ".") == "" {
		return "unnamed"
	}
	// A leading dot would make a hidden file, such as .git.
	if segment[0] == '.' {
		segment = "_" + strings.TrimLeft(segment, ".")
	}
	// Windows treats device names as special even with an extension.
	name, _, _ := strings.Cut(segment, ".")
	if _, ok := reservedDeviceNames[strings.ToUpper(name)]; ok {
		segment = "_" + segment
	}
	segment = truncateBytes(segment, 120)
	segment = strings.TrimRight(segment, " -.")
	if segment == "" {
		return "unnamed"
	}
	return segment
}

// isSafePathRune reports whether a character can be in a file name on all
// common file systems.
func isSafePathRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f || unicode.IsControl(r):
		return false
	case strings.ContainsRune(`<>:"/\|?*`, r):
		return false
	case unicode.Is(unicode.Cf, r) || r == utf8.RuneError:
		// Invisible format characters make names that look equal but differ.
		return false
	}
	return true
}

// truncateBytes cuts s to at most n bytes without splitting a character.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// NormalizeFolderPath splits a provider folder path at "/" into safe folder
// names. Empty pieces, as from a doubled "/", are dropped. "." and ".."
// become normal safe names.
//
// A name that had to be changed gets a short suffix made from the original
// name. So "A:B" and "A?B" don't end up in the same folder, and neither ends
// up in the folder of a real "A-B".
func NormalizeFolderPath(folder string) []string {
	out := []string{}
	for _, part := range strings.Split(folder, "/") {
		if part == "" {
			continue
		}
		segment := SafePathSegment(part)
		if segment != toNFC(part) {
			sum := sha256.Sum256([]byte(toNFC(part)))
			segment += "-" + hex.EncodeToString(sum[:4])
		}
		out = append(out, segment)
	}
	return out
}
