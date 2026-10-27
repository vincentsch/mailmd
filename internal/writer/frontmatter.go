package writer

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vincentsch/mailmd/internal/mail"
)

// renderMarkdownBytes builds the whole file, from the frontmatter to the list
// of attachments. rawName is the .eml file saved next to it, if there is one.
func renderMarkdownBytes(m mail.Message, date time.Time, savedAs []string, body, rawName string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	writeFrontmatter(&b, m, date, savedAs)
	b.WriteString("---\n\n")
	if title := oneLine(m.Subject); title != "" {
		b.WriteString("# " + escapeTitle(title) + "\n\n")
	}
	switch {
	case m.ParseError != "":
		fmt.Fprintf(&b, "_mailmd could not read this message, because %s. The original message is saved as [%s](%s)._\n",
			m.ParseError, escapeLinkText(rawName), linkTarget(rawName))
	case m.OmittedSize > 0:
		fmt.Fprintf(&b, "_mailmd saved only the headers of this message. The message is %s, which is more than the size limit._\n", humanSize(m.OmittedSize))
	case body != "":
		b.WriteString(body)
		b.WriteByte('\n')
	}
	for _, f := range m.Forwarded {
		blankLine(&b)
		b.WriteString(renderForwarded(f))
	}
	writeAttachmentLinks(&b, m.Attachments, savedAs)
	return []byte(b.String())
}

// blankLine ends the text so far with one empty line, so the next heading
// stands apart without a second empty line.
func blankLine(b *strings.Builder) {
	if !strings.HasSuffix(b.String(), "\n\n") {
		b.WriteString("\n")
	}
}

// writeFrontmatter writes the headers a reader looks at first, then the
// fields for threads and tools. Empty fields are left out. The writer writes
// the YAML by hand, so the field order and the quoting never change.
func writeFrontmatter(b *strings.Builder, m mail.Message, date time.Time, savedAs []string) {
	writeScalar(b, "subject", m.Subject)
	writeScalar(b, "from", mail.FormatAddress(m.From))
	writeStringList(b, "to", mail.FormatAddressList(m.To))
	writeStringList(b, "cc", mail.FormatAddressList(m.Cc))
	writeStringList(b, "bcc", mail.FormatAddressList(m.Bcc))
	writeStringList(b, "reply_to", mail.FormatAddressList(m.ReplyTo))
	if !date.IsZero() {
		writeScalar(b, "date", date.Format(time.RFC3339))
	}
	if folders := folderListForFrontmatter(m.Folders); len(folders) == 1 {
		writeScalar(b, "folder", folders[0])
	} else {
		writeStringList(b, "folders", folders)
	}
	writeStringList(b, "labels", m.Labels)
	writeStringList(b, "flags", FlagNames(m.Flags))
	var names []string
	for _, a := range m.Attachments {
		names = append(names, attachmentLabel(a))
	}
	writeStringList(b, "attachments", names)
	if m.OmittedSize > 0 {
		b.WriteString("omitted_bytes: " + strconv.FormatInt(m.OmittedSize, 10) + "\n")
	}
	writeScalar(b, "parse_error", m.ParseError)
	writeScalar(b, "thread_id", m.ThreadID)
	writeScalar(b, "message_id", m.MessageID)
	writeScalar(b, "in_reply_to", m.InReplyTo)
	writeStringList(b, "references", referencesForFrontmatter(m.References))
	writeScalar(b, "ref", m.Ref)
	writeScalar(b, "local_id", m.LocalID)
}

// frontmatterOrder is the order of the fields writeFrontmatter writes.
// deleted_on_server is added later by sync, when the server deleted the
// message.
var frontmatterOrder = []string{
	"subject", "from", "to", "cc", "bcc", "reply_to", "date", "folder", "folders", "labels",
	"flags", "deleted_on_server", "attachments", "omitted_bytes", "parse_error",
	"thread_id", "message_id", "in_reply_to", "references", "ref", "local_id",
}

func writeScalar(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(quoteYAMLString(value))
	b.WriteByte('\n')
}

// writeStringList writes a list on one line, like to: ["a", "b"].
func writeStringList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(key)
	b.WriteString(": [")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteYAMLString(value))
	}
	b.WriteString("]\n")
}

// referencesForFrontmatter writes the references in the header's order,
// repeats included. Only the local_id sorts them and drops repeats.
func referencesForFrontmatter(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// FlagNames lists the flags that are set, like ["seen", "flagged"].
func FlagNames(flags mail.Flags) []string {
	var out []string
	for _, f := range []struct {
		set  bool
		name string
	}{{flags.Seen, "seen"}, {flags.Flagged, "flagged"}, {flags.Answered, "answered"}, {flags.Draft, "draft"}} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// attachmentLabel is the name shown for an attachment.
func attachmentLabel(a mail.Attachment) string {
	if name := strings.TrimSpace(a.Filename); name != "" {
		return name
	}
	return fallbackAttachmentName(a)
}

// writeAttachmentLinks lists the attachments with links to their files, so
// a reader or an agent can open them from the message.
func writeAttachmentLinks(b *strings.Builder, atts []mail.Attachment, savedAs []string) {
	if len(atts) == 0 {
		return
	}
	blankLine(b)
	b.WriteString("## Attachments\n\n")
	for i, a := range atts {
		name := escapeLinkText(attachmentLabel(a))
		details := humanSize(a.Size)
		if a.MIMEType != "" {
			details = a.MIMEType + ", " + details
		}
		if i < len(savedAs) && savedAs[i] != "" {
			fmt.Fprintf(b, "- [%s](%s) (%s)\n", name, linkTarget(savedAs[i]), details)
		} else {
			fmt.Fprintf(b, "- %s (%s, not downloaded)\n", name, details)
		}
	}
}

// renderForwarded shows a forwarded message's headers, its text and the
// names of its attachments, so a search for words in it finds this file.
func renderForwarded(f mail.Message) string {
	var b strings.Builder
	b.WriteString("## Forwarded message\n\n")
	// Its attachments stay inside the forwarded message's .eml file, so only
	// their names are listed.
	var names []string
	for _, a := range f.Attachments {
		names = append(names, attachmentLabel(a))
	}
	for _, h := range []struct{ name, value string }{
		{"From", mail.FormatAddress(f.From)},
		{"Date", formatHeaderDate(f.Date)},
		{"Subject", f.Subject},
		{"To", strings.Join(mail.FormatAddressList(f.To), ", ")},
		{"Attachments", strings.Join(names, ", ")},
	} {
		if h.value != "" {
			fmt.Fprintf(&b, "**%s:** %s  \n", h.name, escapeTitle(h.value))
		}
	}
	text, _ := renderBody(context.Background(), f, nil, false)
	if text != "" {
		b.WriteString("\n" + text + "\n")
	}
	for _, inner := range f.Forwarded {
		b.WriteString("\n" + renderForwarded(inner))
	}
	return b.String()
}

func formatHeaderDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("Mon, 2 Jan 2006 15:04 -0700")
}

// humanSize formats a byte count like 4.2 MB.
func humanSize(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10) + " bytes"
	case n < 1000*1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + " kB"
	case n < 1000*1000*1000:
		return strconv.FormatFloat(float64(n)/1000/1000, 'f', 1, 64) + " MB"
	}
	return strconv.FormatFloat(float64(n)/1000/1000/1000, 'f', 1, 64) + " GB"
}

// escapeTitle stops a subject from turning into Markdown formatting. It adds
// as few backslashes as possible, so the raw text stays readable. It also
// drops control characters.
func escapeTitle(s string) string {
	runes := []rune(s)
	links := strings.Contains(s, "](")
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsControl(r) {
			continue
		}
		escape := false
		switch r {
		case '\\', '`', '*':
			escape = true
		case '<':
			escape = !isAutolink(string(runes[i:min(len(runes), i+300)]))
		case '_':
			// An underscore inside a word, as in file_name, is not emphasis.
			escape = i == 0 || i == len(runes)-1 || !isWordRune(runes[i-1]) || !isWordRune(runes[i+1])
		case '[', ']':
			escape = links
		case '#':
			// A # at the end would close the heading.
			escape = i == len(runes)-1
		}
		if escape {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// oneLine removes control characters and folds white space, so a subject
// fits on one line.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// escapeLinkText stops text inside [ ] from turning into a link.
func escapeLinkText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\[]()<>`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// linkTarget writes a relative link. Paths with spaces or brackets go in
// angle brackets, which CommonMark allows.
func linkTarget(path string) string {
	if strings.ContainsAny(path, " ()<>") {
		return "<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(path) + ">"
	}
	return path
}

// quoteYAMLString writes a value as a YAML string in double quotes, the only
// form the writer uses. Invalid UTF-8 becomes U+FFFD, so broken text from a
// provider can't break the frontmatter.
func quoteYAMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			b.WriteString(`\uFFFD`)
			s = s[1:]
			continue
		}
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if (r >= 0 && r <= 0x1f) || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
		s = s[size:]
	}
	b.WriteByte('"')
	return b.String()
}

// parseQuotedYAMLScalar reads a string written by quoteYAMLString and
// accepts only the escapes that it writes. ok is false for anything else,
// because mailmd didn't write such a value.
func parseQuotedYAMLScalar(src []byte) (string, bool) {
	value, rest, ok := readQuoted(src)
	if !ok || len(bytes.Trim(rest, " \t\r")) > 0 {
		return "", false
	}
	return value, true
}

// parseFlowList reads a list written by writeStringList, like ["a", "b"].
func parseFlowList(src []byte) ([]string, bool) {
	src = bytes.TrimSpace(src)
	if len(src) < 2 || src[0] != '[' {
		return nil, false
	}
	src = src[1:]
	var out []string
	for {
		value, rest, ok := readQuoted(src)
		if !ok {
			return nil, false
		}
		out = append(out, value)
		rest = bytes.TrimLeft(rest, " ")
		switch {
		case bytes.HasPrefix(rest, []byte("]")):
			return out, true
		case bytes.HasPrefix(rest, []byte(",")):
			src = bytes.TrimLeft(rest[1:], " ")
		default:
			return nil, false
		}
	}
}

// readQuoted reads one double-quoted scalar at the start of src, with the
// escapes quoteYAMLString writes, and returns the bytes after it.
func readQuoted(src []byte) (string, []byte, bool) {
	if len(src) == 0 || src[0] != '"' {
		return "", nil, false
	}
	var b strings.Builder
	for i := 1; i < len(src); {
		c := src[i]
		switch c {
		case '"':
			return b.String(), src[i+1:], true
		case '\\':
			if i+1 >= len(src) {
				return "", nil, false
			}
			switch src[i+1] {
			case '\\':
				b.WriteByte('\\')
				i += 2
			case '"':
				b.WriteByte('"')
				i += 2
			case 'n':
				b.WriteByte('\n')
				i += 2
			case 'r':
				b.WriteByte('\r')
				i += 2
			case 't':
				b.WriteByte('\t')
				i += 2
			case 'u':
				if i+6 > len(src) {
					return "", nil, false
				}
				r, ok := parseHex4(src[i+2 : i+6])
				if !ok || !utf8.ValidRune(r) {
					return "", nil, false
				}
				b.WriteRune(r)
				i += 6
			default:
				return "", nil, false
			}
		default:
			r, size := utf8.DecodeRune(src[i:])
			if r == utf8.RuneError && size == 1 {
				return "", nil, false
			}
			b.WriteRune(r)
			i += size
		}
	}
	return "", nil, false
}

func parseHex4(src []byte) (rune, bool) {
	if len(src) != 4 {
		return 0, false
	}
	var v rune
	for _, b := range src {
		var n byte
		switch {
		case b >= '0' && b <= '9':
			n = b - '0'
		case b >= 'A' && b <= 'F':
			n = b - 'A' + 10
		case b >= 'a' && b <= 'f':
			n = b - 'a' + 10
		default:
			return 0, false
		}
		v = v*16 + rune(n)
	}
	return v, true
}
