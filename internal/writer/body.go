package writer

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
	"github.com/vincentsch/mailmd/internal/mail"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlPolicy is built once and only read afterwards.
var htmlPolicy = newHTMLPolicy()

// renderBody returns the message body as Markdown. An invite without any
// text gets its event written out instead. cids maps the Content-IDs of
// inline images to their saved files, so those images can be linked.
func renderBody(ctx context.Context, m mail.Message, cids map[string]string, preferText bool) (string, error) {
	body, err := messageBody(ctx, m, cids, preferText)
	if err == nil && strings.TrimSpace(body) == "" {
		body = calendarBody(m.Attachments)
	}
	return body, err
}

// messageBody converts the body parts. When a message has an HTML part, its
// conversion is used, because the HTML has the links and tables. The plain
// text part is used instead when preferText is set or when there is no HTML
// part. It is also used when the conversion lost most of the text, as with a
// newsletter that is one big image.
func messageBody(ctx context.Context, m mail.Message, cids map[string]string, preferText bool) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	text := mail.NormalizeBodyForRender(m.BodyText)
	if mail.NormalizeBodyForRender(m.BodyHTML) == "" || (preferText && text != "") {
		return escapePlainText(text), nil
	}
	converted, err := sanitizeAndConvertHTML(m.BodyHTML, cids)
	if err != nil && text == "" {
		return "", err
	}
	converted = mail.NormalizeBodyForRender(converted)
	if text != "" && (err != nil || 3*countLetters(linkTargets.ReplaceAllString(converted, "]")) < countLetters(text)) {
		return escapePlainText(text), nil
	}
	return converted, nil
}

// linkTargets matches the addresses of Markdown links and the word that
// marks an image, which are not text a person reads.
var linkTargets = regexp.MustCompile(`\]\([^)]*\)|\[image: `)

// countLetters counts the letters and digits of a text.
func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// escapePlainText stops a plain text body from turning into HTML or images
// when a Markdown viewer shows the file. So opening a message never loads a
// remote image or runs HTML from it. It adds a backslash before a < that
// could start an HTML tag, and before the [ of an image. The rest of the text
// stays as it is, so it is still readable.
func escapePlainText(s string) string {
	if !strings.ContainsAny(s, "<!") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<' && i+1 < len(s) && startsTag(s[i+1]) && !isAutolink(s[i:]):
			b.WriteString(`\<`)
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			b.WriteString(`!\[`)
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// autolink matches an email address or a web address in angle brackets,
// like <dana@example.com>. Markdown shows it as a link. It can't start HTML,
// so it needs no backslash.
var autolink = regexp.MustCompile(`^<(?:[a-zA-Z0-9.!#$%&'*+/=?^_{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*|https?://[^\s<>]+)>`)

// isAutolink reports whether s starts with an autolink. It looks at the
// first 300 bytes only, which is enough for any address.
func isAutolink(s string) bool {
	return autolink.MatchString(s[:min(len(s), 300)])
}

// startsTag reports whether c after a < can begin HTML or an autolink.
func startsTag(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '/' || c == '!' || c == '?'
}

func newHTMLPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"p", "div", "span", "br", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"blockquote", "pre", "code", "kbd", "samp", "tt",
		"ul", "ol", "li", "dl", "dt", "dd",
		"strong", "b", "em", "i", "u", "s", "strike", "del", "ins",
		"sub", "sup", "mark", "small", "q", "cite", "abbr", "figure", "figcaption",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption",
	)
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.SkipElementsContent("script", "style", "head", "title")
	return p
}

// sanitizeAndConvertHTML turns an HTML body into Markdown. It drops hidden
// text like preheaders, and it removes scripts and remote images. Layout
// tables become plain blocks, and data tables stay tables. Inline images
// link to their saved files.
func sanitizeAndConvertHTML(raw string, cids map[string]string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	images := replaceImages(doc, cids, raw)
	removeHidden(doc)
	removeHiddenByStyleSheet(doc)
	flattenLayoutTables(doc)
	demoteHeadings(doc)
	prepared, err := doc.Html()
	if err != nil {
		return "", err
	}
	conv := md.NewConverter("", true, &md.Options{
		HeadingStyle:     "atx",
		HorizontalRule:   "* * *",
		BulletListMarker: "-",
		CodeBlockStyle:   "fenced",
		Fence:            "```",
		EmDelimiter:      "_",
		StrongDelimiter:  "**",
		LinkStyle:        "inlined",
		EscapeMode:       "basic",
	})
	conv.Use(plugin.Table())
	out, err := conv.ConvertString(htmlPolicy.Sanitize(prepared))
	if err != nil {
		return "", err
	}
	// Text like "&lt;img src=...&gt;" comes out of the conversion as a tag,
	// so it is escaped like plain text before the image links go in.
	return images.Replace(escapePlainText(tidyEscapes(out))), nil
}

// tidyEscapes removes two kinds of backslashes that the converter adds but
// Markdown doesn't need. One is before a | outside a table, the other before
// a _ inside a word, as in file_name. So the text reads and searches like
// the original. Code spans and code blocks stay as they are.
func tidyEscapes(md string) string {
	if !strings.Contains(md, `\|`) && !strings.Contains(md, `\_`) {
		return md
	}
	lines := strings.Split(md, "\n")
	fenced := false
	for n, line := range lines {
		trimmed := strings.TrimLeft(line, "> ")
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
		}
		if fenced || !strings.ContainsRune(line, '\\') {
			continue
		}
		table := strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
		var b strings.Builder
		code := false
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c == '`' {
				code = !code
			}
			if c == '\\' && i+1 < len(line) && !code {
				next := line[i+1]
				pipe := next == '|' && !table
				word := next == '_' && i > 0 && i+2 < len(line) && isWordByte(line[i-1]) && isWordByte(line[i+2])
				if pipe || word {
					continue
				}
				// The escaped character is copied as it is, so an escaped
				// backtick doesn't start a code span.
				b.WriteByte(c)
				b.WriteByte(next)
				i++
				continue
			}
			b.WriteByte(c)
		}
		lines[n] = b.String()
	}
	return strings.Join(lines, "\n")
}

// isWordByte reports whether c is part of a word. Bytes of letters outside
// ASCII count as word bytes.
func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// hiddenStyle matches inline styles that hide text, as used for preheaders.
var hiddenStyle = regexp.MustCompile(`display:none|visibility:hidden|mso-hide:all|opacity:0(?:[;!]|$)|max-height:0(?:px)?[;!].*overflow:hidden|overflow:hidden.*max-height:0(?:px)?(?:[;!]|$)`)

// removeHidden deletes elements that a mail client would not show.
func removeHidden(doc *goquery.Document) {
	doc.Find("[hidden], [style]").Each(func(_ int, s *goquery.Selection) {
		if _, hidden := s.Attr("hidden"); hidden {
			s.Remove()
			return
		}
		if hiddenStyle.MatchString(compactStyle(s.AttrOr("style", ""))) {
			s.Remove()
		}
	})
}

// removeHiddenByStyleSheet deletes elements that a rule in a <style> block
// hides, like the .preheader of a newsletter. Only rules at the top level of
// the style sheet count. Rules inside @media switch content between phones
// and desktops, so they are left alone. An inline display style wins over
// the style sheet, like in a mail client.
func removeHiddenByStyleSheet(doc *goquery.Document) {
	var selectors []string
	doc.Find("style").Each(func(_ int, s *goquery.Selection) {
		selectors = append(selectors, hiddenSelectors(s.Text())...)
	})
	for _, selector := range selectors {
		// Selectors that cascadia can't read match nothing.
		doc.Find(selector).Each(func(_ int, el *goquery.Selection) {
			switch goquery.NodeName(el) {
			case "html", "head", "body":
				return
			}
			if style := compactStyle(el.AttrOr("style", "")); strings.Contains(style, "display:") && !hiddenStyle.MatchString(style) {
				return
			}
			el.Remove()
		})
	}
}

// cssComment matches a CSS comment.
var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// hiddenSelectors returns the selectors of the top-level rules in a style
// sheet that hide an element.
func hiddenSelectors(css string) []string {
	css = cssComment.ReplaceAllString(css, "")
	var out []string
	depth, start, body := 0, 0, 0
	selector := ""
	for i := 0; i < len(css); i++ {
		switch css[i] {
		case '{':
			if depth == 0 {
				selector, body = strings.TrimSpace(css[start:i]), i+1
			}
			depth++
		case '}':
			if depth == 0 {
				start = i + 1
				continue
			}
			depth--
			if depth > 0 {
				continue
			}
			start = i + 1
			if strings.HasPrefix(selector, "@") || !hiddenStyle.MatchString(compactStyle(css[body:i])) {
				continue
			}
			for _, one := range strings.Split(selector, ",") {
				if one = strings.TrimSpace(one); one != "" {
					out = append(out, one)
				}
			}
		}
	}
	return out
}

// compactStyle lowers a style and removes its spaces, so hiddenStyle can
// match it.
func compactStyle(style string) string {
	return strings.ToLower(strings.Join(strings.Fields(style), ""))
}

// replaceImages swaps each image for a placeholder word that survives the
// cleanup and the conversion. Inline images become links to their saved
// files. Other images become their alt text, and images without alt text,
// like tracking pixels, are dropped.
func replaceImages(doc *goquery.Document, cids map[string]string, raw string) *strings.Replacer {
	prefix := "MAILMDIMAGE"
	for strings.Contains(raw, prefix) {
		prefix += "X"
	}
	var pairs []string
	doc.Find("img").Each(func(i int, s *goquery.Selection) {
		alt := strings.Join(strings.Fields(s.AttrOr("alt", "")), " ")
		src := strings.TrimSpace(s.AttrOr("src", ""))
		markdown := ""
		if path, ok := cids[normalizeCID(strings.TrimPrefix(strings.TrimPrefix(src, "cid:"), "CID:"))]; ok && strings.HasPrefix(strings.ToLower(src), "cid:") {
			markdown = "![" + escapeLinkText(alt) + "](" + linkTarget(path) + ")"
		} else if alt != "" {
			markdown = "[image: " + escapeLinkText(alt) + "]"
		}
		if markdown == "" {
			s.Remove()
			return
		}
		token := prefix + strconv.Itoa(i) + "END"
		pairs = append(pairs, token, markdown)
		s.ReplaceWithHtml(token)
	})
	return strings.NewReplacer(pairs...)
}

// normalizeCID strips the angle brackets of a Content-ID.
func normalizeCID(cid string) string {
	return strings.Trim(strings.TrimSpace(cid), "<>")
}

// flattenLayoutTables turns tables that only arrange a page into plain
// blocks. Newsletters and receipts nest such tables deeply; as Markdown
// tables they would be unreadable, and dropping them would run cell texts
// together. Tables that hold data stay tables.
func flattenLayoutTables(doc *goquery.Document) {
	tables := doc.Find("table").Nodes
	for i := len(tables) - 1; i >= 0; i-- {
		table := goquery.NewDocumentFromNode(tables[i]).Selection
		if isDataTable(table) {
			prepareDataTable(table)
		} else {
			renameTableTree(tables[i])
		}
	}
}

// isDataTable reports whether a table holds rows of data: a header row, or
// at least two rows with the same number of short cells and no blocks. A
// cell that spans rows can't be shown in a Markdown table, so such a table
// is shown as plain blocks.
func isDataTable(t *goquery.Selection) bool {
	if t.Find("table").Length() > 0 || t.Find("[rowspan]").FilterFunction(func(_ int, c *goquery.Selection) bool { return span(c, "rowspan") > 1 }).Length() > 0 {
		return false
	}
	if t.Find("th").Length() > 0 {
		return true
	}
	rows := t.Find("tr")
	if rows.Length() < 2 {
		return false
	}
	columns := -1
	data := true
	rows.Each(func(_ int, row *goquery.Selection) {
		cells := row.ChildrenFiltered("td")
		if columns == -1 {
			columns = cells.Length()
		}
		if cells.Length() != columns || cells.Length() < 2 {
			data = false
		}
		cells.Each(func(_ int, cell *goquery.Selection) {
			if _, span := cell.Attr("colspan"); span || cell.Find("p, div, ul, ol, h1, h2, h3, h4, img, br").Length() > 0 || len(strings.TrimSpace(cell.Text())) > 200 {
				data = false
			}
		})
	})
	return data
}

// prepareDataTable makes a data table fit a Markdown table. A cell that
// spans several columns gets empty cells after it, so the next cells stay in
// their columns, like a receipt's total under "Amount". A table without
// header cells gets its first row as the header, because a Markdown table
// always has one.
func prepareDataTable(t *goquery.Selection) {
	t.Find("td, th").Each(func(_ int, cell *goquery.Selection) {
		n := span(cell, "colspan")
		cell.RemoveAttr("colspan")
		for ; n > 1; n-- {
			cell.AfterHtml("<" + goquery.NodeName(cell) + "></" + goquery.NodeName(cell) + ">")
		}
	})
	if t.Find("th").Length() == 0 {
		t.Find("tr").First().ChildrenFiltered("td").Each(func(_ int, cell *goquery.Selection) {
			cell.Nodes[0].Data, cell.Nodes[0].DataAtom = "th", atom.Th
		})
	}
}

// span reads a colspan or rowspan attribute. Missing or odd values count
// as 1, and very large ones are capped.
func span(cell *goquery.Selection, attr string) int {
	n, err := strconv.Atoi(strings.TrimSpace(cell.AttrOr(attr, "1")))
	if err != nil || n < 1 {
		return 1
	}
	return min(n, 20)
}

// demoteHeadings moves every heading one level down, so the subject stays
// the only top-level heading of the file.
func demoteHeadings(doc *goquery.Document) {
	next := map[atom.Atom]atom.Atom{atom.H1: atom.H2, atom.H2: atom.H3, atom.H3: atom.H4, atom.H4: atom.H5, atom.H5: atom.H6}
	for _, n := range doc.Find("h1, h2, h3, h4, h5").Nodes {
		n.DataAtom = next[n.DataAtom]
		n.Data = n.DataAtom.String()
	}
}

// renameTableTree turns a table and its rows and cells into divs. Nested
// tables were already handled.
func renameTableTree(n *html.Node) {
	switch n.DataAtom {
	case atom.Table, atom.Thead, atom.Tbody, atom.Tfoot, atom.Tr, atom.Td, atom.Th, atom.Caption:
		n.Data, n.DataAtom = "div", atom.Div
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom != atom.Table {
			renameTableTree(c)
		}
	}
}
