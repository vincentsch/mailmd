package writer

import (
	"testing"
)

func TestQuoteYAMLString(t *testing.T) {
	raw := "slash\\ quote\" lf\n cr\r tab\t nul\x00 unit\x1f del\x7f café " + string([]byte{0xff})
	want := `"slash\\ quote\" lf\n cr\r tab\t nul\u0000 unit\u001F del\u007F café \uFFFD"`
	if got := quoteYAMLString(raw); got != want {
		t.Fatalf("quoteYAMLString = %q, want %q", got, want)
	}
}

func TestOwnedFile(t *testing.T) {
	const id = "mailmd-localid-v1-abc"
	for _, tt := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"same message", "---\nsubject: \"x\"\nlocal_id: \"" + id + "\"\n---\nbody", true},
		{"other message", "---\nlocal_id: \"mailmd-localid-v1-other\"\n---\n", false},
		{"empty", "", true},
		{"zero filled", "\x00\x00\x00", true},
		{"cut off in frontmatter", "---\nsubject: \"Hel", true},
		{"cut off in local_id", "---\nsubject: \"x\"\nlocal_id: \"mailmd-loc", true},
		{"no frontmatter", "plain text\n", false},
		{"frontmatter without local_id", "---\nsubject: \"x\"\n---\n", false},
		{"indented local_id", "---\n  local_id: \"" + id + "\"\n---\n", false},
		{"broken local_id line", "---\nlocal_id: \"abc\\x\"\n---\n", false},
	} {
		if got := ownedFile([]byte(tt.in), id); got != tt.ok {
			t.Errorf("%s: ownedFile = %v, want %v", tt.name, got, tt.ok)
		}
	}
}
