package mail

import (
	"strings"
	"testing"
)

func TestNormalizeBodyForRender(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "lf", in: "a\nb\n", want: "a\nb"},
		{name: "crlf", in: "a\r\nb\r\n", want: "a\nb"},
		{name: "cr", in: "a\rb\r", want: "a\nb"},
		{name: "trailing run stripped", in: "a\n\n\n", want: "a"},
		{name: "leading and interior blank lines preserved", in: "\n\na\n\nb\n", want: "\n\na\n\nb"},
		{name: "trailing spaces preserved", in: "a  \t", want: "a  \t"},
		{name: "only line endings", in: "\r\n\r\n", want: ""},
		{name: "only spaces", in: " \t  ", want: " \t  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeBodyForRender(tt.in); got != tt.want {
				t.Fatalf("NormalizeBodyForRender(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSubjectSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "Re: [PATCH] Fix bug (v2)", want: "re-patch-fix-bug-v2"},
		{in: "Hello, World!", want: "hello-world"},
		{in: "Résumé", want: "résumé"},
		{in: "你好世界", want: "你好世界"},
		{in: "Grüße aus München – Übergabe", want: "grüße-aus-münchen-übergabe"},
		{in: "会議の議事録 🎉 Meeting notes", want: "会議の議事録-meeting-notes"},
		{in: "Отчёт за март", want: "отчёт-за-март"},
		{in: "", want: "no-subject"},
		{in: "Wait... what?!", want: "wait-what"},
		{in: "a\t \n b", want: "a-b"},
		{in: "---", want: "no-subject"},
	}

	for _, tt := range tests {
		if got := SubjectSlug(tt.in); got != tt.want {
			t.Fatalf("SubjectSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	long := strings.Repeat("a-", 60)
	got := SubjectSlug(long)
	if len(got) > 80 {
		t.Fatalf("SubjectSlug(long) length = %d", len(got))
	}
	if strings.HasSuffix(got, "-") {
		t.Fatalf("SubjectSlug(long) has trailing hyphen: %q", got)
	}
}
