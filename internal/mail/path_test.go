package mail

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafePathSegment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: ".mailmd", want: "_mailmd"},
		{in: ".git", want: "_git"},
		{in: "..", want: "unnamed"},
		{in: "CON", want: "_CON"},
		{in: "CON.txt.bak", want: "_CON.txt.bak"},
		{in: "NUL.tar.gz", want: "_NUL.tar.gz"},
		{in: "Quarterly: report?.pdf", want: "Quarterly- report-.pdf"},
		{in: "Résumé", want: "Résumé"},
		{in: "Rechnungen Ü", want: "Rechnungen Ü"},
		{in: "仕事", want: "仕事"},
		{in: "Отчёты  2026", want: "Отчёты 2026"},
		{in: "[Gmail]", want: "[Gmail]"},
		{in: "zero\u200bwidth", want: "zero-width"},
		{in: "alpha---beta", want: "alpha-beta"},
		{in: "../secret", want: "_-secret"},
		{in: `a/b\c`, want: "a-b-c"},
		{in: "a\x00b\x1fc", want: "a-b-c"},
		{in: "name...   ", want: "name"},
		{in: "COM0", want: "COM0"},
		{in: "LPT0.txt", want: "LPT0.txt"},
		{in: "COM.txt", want: "COM.txt"},
		{in: "你好", want: "你好"},
		{in: "?*:", want: "unnamed"},
	}

	for _, tt := range tests {
		if got := SafePathSegment(tt.in); got != tt.want {
			t.Fatalf("SafePathSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	long := strings.Repeat("a", 119) + "-."
	got := SafePathSegment(long + "tail")
	if len(got) > 120 {
		t.Fatalf("SafePathSegment(long) length = %d", len(got))
	}
	if cjk := SafePathSegment(strings.Repeat("仕", 60)); len(cjk) > 120 || !utf8.ValidString(cjk) {
		t.Fatalf("SafePathSegment(CJK) = %q", cjk)
	}
	if strings.HasSuffix(got, " ") || strings.HasSuffix(got, "-") || strings.HasSuffix(got, ".") {
		t.Fatalf("SafePathSegment(long) has unsafe suffix: %q", got)
	}

	if got, want := SafePathSegment("Re\u0301sume\u0301"), SafePathSegment("Résumé"); got != want {
		t.Fatalf("NFC/NFD outputs differ: %q vs %q", got, want)
	}
}

func TestChangedFolderNamesDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, name := range []string{"A:B", "A?B", "A-B", "a:b"} {
		key := strings.Join(NormalizeFolderPath(name), "/")
		if other, ok := seen[key]; ok {
			t.Fatalf("%q and %q both map to %q", name, other, key)
		}
		seen[key] = name
	}
}

func TestNormalizeFolderPath(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: []string{}},
		{in: "/Inbox//Work/", want: []string{"Inbox", "Work"}},
		{in: "./../Inbox", want: []string{"unnamed-cdb4ee2a", "unnamed-5ec1f7e7", "Inbox"}},
		{in: "Projects/Quarterly: report?", want: []string{"Projects", "Quarterly- report-4b0807d9"}},
		{in: "   ", want: []string{"unnamed-0aad7da7"}},
		{in: "Rechnungen Ü/仕事", want: []string{"Rechnungen Ü", "仕事"}},
	}

	for _, tt := range tests {
		got := NormalizeFolderPath(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("NormalizeFolderPath(%q) = %#v, want %#v", tt.in, got, tt.want)
		}
		for _, segment := range got {
			if strings.ContainsAny(segment, `/\`) {
				t.Fatalf("NormalizeFolderPath(%q) returned separator segment %q", tt.in, segment)
			}
		}
	}
}
