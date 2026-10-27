package rfc822

import (
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	msgmail "github.com/emersion/go-message/mail"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
	"golang.org/x/text/encoding/charmap"
)

// partResult holds the best text and HTML body found in a part. The booleans
// tell an empty body apart from no body of that type.
type partResult struct {
	text    string
	hasText bool
	html    string
	hasHTML bool
}

// walker collects attachments and forwarded messages while it walks the
// MIME tree of one message.
type walker struct {
	depth       int
	attachments []mdmail.Attachment
	forwarded   []mdmail.Message
}

// walk visits the parts depth first. It reads each child's body before it
// asks for the next child, because the multipart reader streams. Any error
// stops the whole message, so a tree that was read only in part never gets a
// wrong local ID.
func (w *walker) walk(entity *message.Entity) (partResult, error) {
	if entity == nil {
		return partResult{}, parseFailure(reasonStructure)
	}
	mediaType, _, _ := entity.Header.ContentType()
	mediaType = strings.ToLower(mediaType)
	if mr := entity.MultipartReader(); mr != nil {
		defer mr.Close()
		children := []partResult{}
		for {
			child, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return partResult{}, parseFailure(reasonCutOff)
			}
			if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
				return partResult{}, parseFailure(reasonStructure)
			}
			if child == nil {
				return partResult{}, parseFailure(reasonStructure)
			}
			res, err := w.walk(child)
			if err != nil {
				return partResult{}, err
			}
			children = append(children, res)
		}
		if mediaType == "multipart/alternative" {
			return lastAlternative(children), nil
		}
		return joinParts(children), nil
	}
	return w.leaf(entity, mediaType)
}

// lastAlternative takes the last text and the last HTML version of a
// multipart/alternative, because RFC 2046 puts the best version last.
func lastAlternative(children []partResult) partResult {
	var out partResult
	for _, child := range children {
		if child.hasText {
			out.text = child.text
			out.hasText = true
		}
		if child.hasHTML {
			out.html = child.html
			out.hasHTML = true
		}
	}
	return out
}

// joinParts reads the body parts of a multipart/mixed or similar container
// one after the other, in their order, so no part is lost. A mail client
// shows all of them, like a text part followed by a mailing list footer.
//
// The text body is the parts' text joined, but only when every part has
// text. The HTML body is the parts' HTML joined, with text-only parts
// escaped into it. So whichever body the writer picks, it holds all parts.
func joinParts(children []partResult) partResult {
	var out partResult
	var texts, htmls []string
	allText, anyHTML := true, false
	for _, child := range children {
		if !child.hasText && !child.hasHTML {
			continue
		}
		allText = allText && child.hasText
		anyHTML = anyHTML || child.hasHTML
	}
	for _, child := range children {
		switch {
		case child.hasHTML:
			htmls = append(htmls, child.html)
		case child.hasText:
			htmls = append(htmls, textAsHTML(child.text))
		}
		if child.hasText {
			texts = append(texts, child.text)
		}
	}
	if len(texts) > 0 && allText {
		out.text, out.hasText = strings.Join(texts, "\n\n"), true
	}
	if anyHTML {
		out.html, out.hasHTML = strings.Join(htmls, "\n"), true
	}
	return out
}

// textAsHTML turns a plain text part into HTML that shows the same text with
// the same line breaks.
func textAsHTML(text string) string {
	escaped := html.EscapeString(strings.ReplaceAll(text, "\r\n", "\n"))
	return "<div>" + strings.ReplaceAll(escaped, "\n", "<br>\n") + "</div>"
}

// leaf treats plain and HTML parts without a file name as body candidates.
// Everything else is an attachment. A forwarded message stays an attachment
// and is also parsed, so its text can be shown.
func (w *walker) leaf(entity *message.Entity, mediaType string) (partResult, error) {
	disp, _, _ := entity.Header.ContentDisposition()
	disp = strings.ToLower(disp)
	filename := attachmentName(entity.Header)
	data, err := io.ReadAll(entity.Body)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return partResult{}, parseFailure(reasonCutOff)
	}
	if err != nil {
		return partResult{}, parseFailure(reasonEncoding)
	}
	isText := mediaType == "text/plain" || mediaType == "text/html"
	if isText && disp != "attachment" && filename == "" {
		text := fixText(string(data))
		if mediaType == "text/html" {
			return partResult{html: text, hasHTML: true}, nil
		}
		return partResult{text: text, hasText: true}, nil
	}
	if data == nil {
		data = []byte{}
	}
	if mediaType == "message/rfc822" && w.depth < maxForwardDepth {
		if inner, err := normalize(data, w.depth+1); err == nil {
			inner.LocalID = ""
			w.forwarded = append(w.forwarded, inner)
		}
	}
	w.attachments = append(w.attachments, mdmail.Attachment{
		Filename:  filename,
		MIMEType:  mediaType,
		Size:      int64(len(data)),
		ContentID: trimASCIISpace(entity.Header.Get("Content-Id")),
		Inline:    disp == "inline",
		Data:      data,
	})
	return partResult{}, nil
}

// attachmentName reads the file name of a part. Some mailers put an encoded
// word in quotes, or write a name with spaces and no quotes at all. The
// standard parser gives up on the second, so the name is then read by hand.
func attachmentName(h message.Header) string {
	ah := msgmail.AttachmentHeader{Header: h}
	if name, _ := ah.Filename(); name != "" {
		return decodeText(name)
	}
	for _, field := range []string{"Content-Disposition", "Content-Type"} {
		if m := looseName.FindStringSubmatch(h.Get(field)); m != nil {
			return decodeText(strings.TrimSpace(m[1] + m[2]))
		}
	}
	return ""
}

// looseName finds a filename or name parameter that mime.ParseMediaType
// rejected.
var looseName = regexp.MustCompile(`(?i);\s*(?:file)?name\s*=\s*(?:"([^"]*)"|([^;]*))`)

// fixText repairs text that is not valid UTF-8. Mail without a declared
// character set is most often Windows-1252, so the text is read as that.
func fixText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	fixed, err := charmap.Windows1252.NewDecoder().String(s)
	if err != nil {
		return strings.ToValidUTF8(s, "\uFFFD")
	}
	return fixed
}
