package rfc822

import (
	"bytes"
	"errors"
	"io"
	"mime"
	netmail "net/mail"
	"net/textproto"
	"strings"
	"unicode"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	msgtext "github.com/emersion/go-message/textproto"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

// Normalize parses one raw RFC 822 message into a mail.Message.
//
// An unknown charset or transfer encoding is not an error. go-message then
// passes the text on as it is. Errors carry only a fixed reason, never data
// from the message.
func Normalize(raw []byte) (mdmail.Message, error) {
	return normalize(raw, 0)
}

// maxForwardDepth limits how deep forwarded messages inside forwarded
// messages are read.
const maxForwardDepth = 3

func normalize(raw []byte, depth int) (mdmail.Message, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return mdmail.Message{}, parseFailure(reasonHeader)
	}
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return mdmail.Message{}, parseFailure(reasonHeader)
	}
	msg := headerFields(entity)
	w := walker{depth: depth}
	body, err := w.walk(entity)
	if err != nil {
		return mdmail.Message{}, err
	}
	msg.BodyText = body.text
	msg.BodyHTML = body.html
	msg.Attachments = w.attachments
	msg.Forwarded = w.forwarded
	return withLocalID(msg)
}

// NormalizeHeaders builds a message from the header section alone. Backends
// use it for a message that is larger than the size limit. size is the full
// size of that message.
func NormalizeHeaders(header []byte, size int64) (mdmail.Message, error) {
	entity, err := message.Read(bytes.NewReader(header))
	if entity == nil {
		return mdmail.Message{}, parseFailure(reasonHeader)
	}
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return mdmail.Message{}, parseFailure(reasonHeader)
	}
	msg := headerFields(entity)
	msg.OmittedSize = size
	return withLocalID(msg)
}

func headerFields(entity *message.Entity) mdmail.Message {
	rawHeaders := make(map[string][]string)
	for fields := entity.Header.Fields(); fields.Next(); {
		key := textproto.CanonicalMIMEHeaderKey(fields.Key())
		rawHeaders[key] = append(rawHeaders[key], fields.Value())
	}
	date, _ := netmail.ParseDate(entity.Header.Get("Date"))
	msg := mdmail.Message{
		MessageID:  trimASCIISpace(entity.Header.Get("Message-Id")),
		InReplyTo:  trimASCIISpace(entity.Header.Get("In-Reply-To")),
		References: parseReferences(entity.Header.Get("References")),
		To:         parseAddressList(entity.Header.Get("To")),
		Cc:         parseAddressList(entity.Header.Get("Cc")),
		Bcc:        parseAddressList(entity.Header.Get("Bcc")),
		ReplyTo:    parseAddressList(entity.Header.Get("Reply-To")),
		Subject:    decodeText(entity.Header.Get("Subject")),
		Date:       date,
		RawHeaders: rawHeaders,
	}
	if from := parseAddressList(entity.Header.Get("From")); len(from) > 0 {
		msg.From = from[0]
	}
	msg.ThreadID = threadRoot(msg)
	return msg
}

// threadRoot names the conversation a message belongs to: the first message
// of its References chain, else the message it replies to, else itself.
// Replies in INBOX and Sent then share one thread ID. Sync can still join a
// message to a thread it knows when References was cut short.
func threadRoot(m mdmail.Message) string {
	if len(m.References) > 0 {
		return m.References[0]
	}
	// strings.Fields also splits at Unicode spaces, so a header of only a
	// no-break space gives no fields.
	if parent := strings.Fields(m.InReplyTo); len(parent) > 0 {
		return parent[0]
	}
	return m.MessageID
}

// Unreadable builds a message for raw bytes that Normalize could not read,
// so the message is never left out. It has every header that can be read
// and the original bytes, which the writer saves as an .eml file. Its
// local_id comes from the raw bytes, so two different broken messages never
// share a file.
func Unreadable(raw []byte, cause error) mdmail.Message {
	reason := reasonHeader
	switch {
	case errors.Is(cause, ErrIdentity):
		reason = reasonEmpty
	case cause != nil:
		if _, detail, ok := strings.Cut(cause.Error(), ErrParse.Error()+": "); ok {
			reason = detail
		}
	}
	var msg mdmail.Message
	if entity, _ := message.New(message.Header{Header: lenientHeader(raw)}, bytes.NewReader(nil)); entity != nil {
		msg = headerFields(entity)
	}
	msg.ParseError = reason
	msg.Raw = raw
	msg.LocalID = mdmail.RawLocalID(raw)
	return msg
}

// lenientHeader reads the header section of a broken message line by line.
// Lines that are not "Name: value" are ignored.
func lenientHeader(raw []byte) msgtext.Header {
	block := raw
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := bytes.Index(raw, []byte(sep)); i >= 0 && i < len(block) {
			block = raw[:i]
		}
	}
	if len(block) > headerLimit {
		block = block[:headerLimit]
	}
	var fields [][2]string
	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			if n := len(fields); n > 0 {
				fields[n-1][1] += " " + strings.TrimSpace(line)
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && name != "" && !strings.ContainsAny(name, " \t") {
			fields = append(fields, [2]string{name, strings.TrimSpace(value)})
		}
	}
	// go-message stores fields last to first, so they are added in reverse.
	var h msgtext.Header
	for i := len(fields) - 1; i >= 0; i-- {
		h.Add(fields[i][0], fields[i][1])
	}
	return h
}

// headerLimit is how much of a broken message is read as headers.
const headerLimit = 1 << 20

func withLocalID(msg mdmail.Message) (mdmail.Message, error) {
	msg, err := mdmail.EnsureLocalID(msg)
	if errors.Is(err, mdmail.ErrInsufficientIdentity) {
		return mdmail.Message{}, ErrIdentity
	}
	if err != nil {
		return mdmail.Message{}, parseFailure(reasonHeader)
	}
	return msg, nil
}

// parseReferences splits References at white space and doesn't check the
// IDs. They are only compared with other message IDs.
func parseReferences(raw string) []string {
	return strings.FieldsFunc(raw, unicode.IsSpace)
}

// wordDecoder decodes RFC 2047 encoded words, like =?utf-8?q?M=C3=BCller?=.
// It forgives more than go-message's decoder. It ignores a language tag
// after the charset, as in utf-8*de. The bytes of an unknown charset stay as
// they are, so fixText can repair them.
var wordDecoder = &mime.WordDecoder{CharsetReader: func(name string, input io.Reader) (io.Reader, error) {
	name, _, _ = strings.Cut(name, "*")
	if r, err := charset.Reader(name, input); err == nil {
		return r, nil
	}
	return input, nil
}}

var addressParser = &netmail.AddressParser{WordDecoder: wordDecoder}

// decodeText decodes the encoded words of a header like Subject.
func decodeText(s string) string {
	if decoded, err := wordDecoder.DecodeHeader(s); err == nil {
		s = decoded
	}
	return fixText(s)
}

// parseAddressList reads an address list and decodes the display names.
// Some mailers encode the whole header, addresses included, as one encoded
// word. Such a list only parses after decoding, so that is tried next. When
// both fail, the simpler parser of the mail package reads what it can, so a
// broken address list doesn't stop the whole message.
func parseAddressList(raw string) []mdmail.Address {
	t := trimASCIISpace(raw)
	if t == "" {
		return []mdmail.Address{}
	}
	list, err := addressParser.ParseList(t)
	if err != nil {
		decoded := decodeText(t)
		if list, err = addressParser.ParseList(decoded); err != nil {
			return mdmail.ParseAddressList(decoded)
		}
	}
	out := make([]mdmail.Address, 0, len(list))
	for _, a := range list {
		if a == nil {
			continue
		}
		// The parser leaves encoded words inside quotes, and words written
		// without a space between them, as they are.
		out = append(out, mdmail.Address{Name: decodeText(a.Name), Email: a.Address})
	}
	return out
}

// trimASCIISpace trims only ASCII white space. Other Unicode spaces stay,
// because the local ID is computed from these values.
func trimASCIISpace(s string) string {
	start := 0
	for start < len(s) && isASCIISpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isASCIISpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}
