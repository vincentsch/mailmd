package writer

import (
	"context"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/mail"
)

func TestSanitizeAndConvertHTML(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<h2>Hi</h2><p><a href="https://example.com">safe</a> <a href="javascript:alert(1)">bad</a> <a href="data:text/html,no">data</a> <a href="//example.com/x">proto</a> <a href="/rel">rel</a><img src="cid:x" alt=" Logo&#10; Mark "><img src="https://tracker.example/p.gif" width="1" height="1"></p><script>evil()</script><style>.x{}</style><blockquote>Quote</blockquote><ul><li>One</li></ul>`, nil)
	if err != nil {
		t.Fatalf("sanitizeAndConvertHTML: %v", err)
	}
	for _, forbidden := range []string{"evil", "tracker", "javascript:", "data:text", "](//example.com", "](/rel)", "cid:"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output contains forbidden %q:\n%s", forbidden, out)
		}
	}
	for _, want := range []string{"## Hi", "[safe](https://example.com)", "bad", "data", "proto", "rel", "[image: Logo Mark]", "> Quote", "- One"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	out2, err := sanitizeAndConvertHTML(`<p>same<img alt="x"></p>`, nil)
	if err != nil {
		t.Fatalf("sanitizeAndConvertHTML second: %v", err)
	}
	out3, err := sanitizeAndConvertHTML(`<p>same<img alt="x"></p>`, nil)
	if err != nil {
		t.Fatalf("sanitizeAndConvertHTML third: %v", err)
	}
	if out2 != out3 {
		t.Fatalf("HTML conversion is nondeterministic:\n%q\n%q", out2, out3)
	}
}

func TestImageAltMarkdownIsEscaped(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<p><img alt="x](javascript:alert(1)) <javascript:alert(2)> [ok](https://example.com) \ slash"></p>`, nil)
	if err != nil {
		t.Fatalf("sanitizeAndConvertHTML: %v", err)
	}
	if strings.Contains(out, "](javascript:") || strings.Contains(out, "[ok](https://example.com)") {
		t.Fatalf("hostile alt text created active Markdown syntax:\n%s", out)
	}
	want := `[image: x\]\(javascript:alert\(1\)\) \<javascript:alert\(2\)\> \[ok\]\(https://example.com\) \\ slash]`
	if out != want {
		t.Fatalf("escaped alt output = %q, want %q", out, want)
	}
}

func TestHTMLTablesPreheadersAndInlineImages(t *testing.T) {
	html := `<html><body>
<div style="display: none; max-height:0px">Preheader text you should not see</div>
<span style="mso-hide:all">Also hidden</span>
<table width="600"><tr><td>
  <h1>Receipt</h1>
  <table><tr><td>Pro plan (monthly)</td><td>1</td><td>$49.00</td></tr><tr><td>Seats</td><td>3</td><td>$27.00</td></tr></table>
  <table border="1"><tr><th>Package</th><th>Stars</th></tr><tr><td>cobra</td><td>38k</td></tr></table>
  <p><img src="cid:logo@example" alt="Logo"> <img src="https://track.example/open.gif" width="1" height="1"></p>
</td></tr></table>
</body></html>`
	out, err := sanitizeAndConvertHTML(html, map[string]string{"logo@example": "x.attachments/01-logo.png"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Preheader", "Also hidden", "track.example", "Pro plan (monthly)1"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output contains %q:\n%s", forbidden, out)
		}
	}
	for _, want := range []string{"# Receipt", "| Pro plan (monthly) | 1 | $49.00 |", "| Package | Stars |", "| cobra | 38k |", "![Logo](x.attachments/01-logo.png)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestLayoutTableCellsStayApart(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<table><tr><td><p>Hello Dana</p></td></tr><tr><td><div>Your order</div></td><td><div>shipped</div></td></tr></table>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "|") || strings.Contains(out, "Your ordershipped") || !strings.Contains(out, "Hello Dana") {
		t.Fatalf("layout table output:\n%s", out)
	}
}

func TestBodyPrefersConvertedHTMLAndFallsBackToText(t *testing.T) {
	ctx := context.Background()
	both := mail.Message{
		BodyText: "Read the report at https://example.com/r\n",
		BodyHTML: `<p>Read <a href="https://example.com/r">the report</a>.</p><table><tr><th>Item</th><th>Price</th></tr><tr><td>Pro</td><td>$49</td></tr></table>`,
	}
	got, _ := renderBody(ctx, both, nil, false)
	if !strings.Contains(got, "[the report](https://example.com/r)") || !strings.Contains(got, "| Pro | $49 |") {
		t.Fatalf("HTML body = %q", got)
	}
	if got, _ := renderBody(ctx, both, nil, true); got != "Read the report at https://example.com/r" {
		t.Fatalf("text body with preferText = %q", got)
	}
	// A newsletter that is one big image keeps its words only in the text
	// part.
	image := mail.Message{
		BodyText: "October news: we moved the office to Berlin and hired three people.",
		BodyHTML: `<a href="https://example.com"><img src="https://example.com/news.png" alt="News"></a>`,
	}
	if got, _ := renderBody(ctx, image, nil, false); !strings.Contains(got, "moved the office") {
		t.Fatalf("image-only HTML body = %q", got)
	}
}

func TestPlainTextCannotLoadImagesOrRunHTML(t *testing.T) {
	// An address in angle brackets can only become a link, so it stays as
	// it is.
	text := "Hi <bob@example.com>, see <https://example.com/a>\n![tracking](https://tracker.example.com/open)\n<script>alert(1)</script><img src=https://x.example/p.gif>\n<javascript:alert(1)>\na < b and 3<4\n> quoted line"
	got, _ := renderBody(context.Background(), mail.Message{BodyText: text}, nil, false)
	for _, bad := range []string{"![tracking]", "<script>", "<img", "<javascript"} {
		if strings.Contains(strings.ReplaceAll(got, `\<`, ""), bad) {
			t.Fatalf("plain text keeps %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{`Hi <bob@example.com>, see <https://example.com/a>`, `!\[tracking](https://tracker.example.com/open)`, `\<script>alert(1)\</script>`, "a < b and 3<4", "> quoted line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain text misses %q:\n%s", want, got)
		}
	}
	// Text in an HTML body that looks like a tag also stays text.
	html, _ := sanitizeAndConvertHTML(`<p>Use &lt;img src="https://tracker.example.com/p.gif"&gt; here</p>`, nil)
	if strings.Contains(strings.ReplaceAll(html, `\<`, ""), "<img") {
		t.Fatalf("converted HTML = %q", html)
	}
}

func TestColspanKeepsCellsInTheirColumns(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<table>
<tr><th>Description</th><th>Qty</th><th>Amount</th></tr>
<tr><td>Pro plan</td><td>1</td><td>$49.00</td></tr>
<tr><td colspan="2"><b>Total</b></td><td><b>$76.00</b></td></tr>
</table>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "| **Total** |  | **$76.00** |") {
		t.Fatalf("receipt table:\n%s", out)
	}
	merged, _ := sanitizeAndConvertHTML(`<table><tr><th>Day</th><th>Task</th></tr><tr><td rowspan="2">Mon</td><td>Write</td></tr><tr><td>Test</td></tr></table>`, nil)
	if strings.Contains(merged, "| Mon |") || !strings.Contains(merged, "Mon") || !strings.Contains(merged, "Test") {
		t.Fatalf("a table with rowspan is shown as blocks:\n%s", merged)
	}
	noHeader, _ := sanitizeAndConvertHTML(`<table><tr><td>Name</td><td>Size</td></tr><tr><td>a.pdf</td><td>1 MB</td></tr></table>`, nil)
	if !strings.HasPrefix(strings.TrimSpace(noHeader), "| Name | Size |") || strings.Contains(noHeader, "|  |  |") {
		t.Fatalf("table without header cells:\n%s", noHeader)
	}
}

func TestHTMLHeadingsMoveBelowTheSubject(t *testing.T) {
	out, _ := sanitizeAndConvertHTML(`<h1>The Weekly Digest</h1><h2>News</h2>`, nil)
	if !strings.Contains(out, "## The Weekly Digest") || !strings.Contains(out, "### News") || strings.Contains(out, "\n# ") || strings.HasPrefix(out, "# ") {
		t.Fatalf("headings:\n%s", out)
	}
}

func TestConvertedTextHasNoNeedlessBackslashes(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<p>Home | Account | Help</p><p>Rename my_old_var in 15_000 rows, from Dana &lt;dana@example.com&gt;</p>`+
		`<p>Keep <code>a\|b and x_y</code> and _this_ alone</p><table><tr><th>Cmd</th></tr><tr><td>a|b</td></tr></table><pre><code>grep 'a\|b' my\_file</code></pre>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Home | Account | Help",
		"Rename my_old_var in 15_000 rows, from Dana <dana@example.com>",
		"`a\\|b and x_y`", `\_this\_ alone`,
		`| a\|b |`,
		`grep 'a\|b' my\_file`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTextHiddenByAStyleSheetIsDropped(t *testing.T) {
	out, err := sanitizeAndConvertHTML(`<html><head><style type="text/css">
/* hide the preview text */
.preheader { display: none !important; max-height: 0; }
#tracking, td.spacer-text{visibility:hidden}
.desktop-only { color: #333 }
@media only screen and (max-width: 600px) { .desktop-only { display: none !important; } }
p:hover::after { display:none }
.shown { display:none }
</style></head><body>
<div class="preheader">Preview text you should not see</div>
<span id="tracking">Tracking words</span>
<table><tr><td class="spacer-text">Spacer words</td><td>Cell</td></tr></table>
<p class="desktop-only">Desktop text stays</p>
<p class="shown" style="display:block">Inline style wins</p>
<p>Main text</p>
</body></html>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"Preview text", "Tracking words", "Spacer words"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q is still there:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"Cell", "Desktop text stays", "Inline style wins", "Main text"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q is missing:\n%s", kept, out)
		}
	}
}
