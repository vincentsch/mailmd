package mail

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Address
	}{
		{name: "display name", raw: "Alice <alice@example.com>", want: Address{Name: "Alice", Email: "alice@example.com"}},
		{name: "quoted name", raw: `"Alice Q." <alice@example.com>`, want: Address{Name: "Alice Q.", Email: "alice@example.com"}},
		{name: "encoded name", raw: "=?utf-8?q?J=C3=B6rg?= <joerg@example.com>", want: Address{Name: "Jörg", Email: "joerg@example.com"}},
		{name: "malformed", raw: "  not < closed  ", want: Address{Name: "not < closed"}},
		{name: "empty", raw: "\t \n", want: Address{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseAddress(tt.raw); got != tt.want {
				t.Fatalf("ParseAddress(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseAddressList(t *testing.T) {
	raw := `Alice <alice@example.com>, "Bob B." <bob@example.com>`
	want := []Address{
		{Name: "Alice", Email: "alice@example.com"},
		{Name: "Bob B.", Email: "bob@example.com"},
	}
	if got := ParseAddressList(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAddressList() = %#v, want %#v", got, want)
	}
	if got := ParseAddressList("bad < list"); !reflect.DeepEqual(got, []Address{{Name: "bad < list"}}) {
		t.Fatalf("ParseAddressList(malformed) = %#v", got)
	}
	if got := ParseAddressList("\n\t"); got == nil || len(got) != 0 {
		t.Fatalf("ParseAddressList(empty) = %#v", got)
	}

	first := ParseAddressList("Friends: alice@example.com, bob@example.com;")
	second := ParseAddressList("Friends: alice@example.com, bob@example.com;")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ParseAddressList(group) is nondeterministic: %#v vs %#v", first, second)
	}
}

func TestFormatAddress(t *testing.T) {
	tests := []struct {
		in   Address
		want string
	}{
		{in: Address{Name: "Alice", Email: "alice@example.com"}, want: "Alice <alice@example.com>"},
		{in: Address{Email: " alice@example.com \n"}, want: "alice@example.com"},
		{in: Address{Name: " Alice \n B. \t Example "}, want: "Alice B. Example"},
		{in: Address{Name: "Jörg", Email: "joerg@example.com"}, want: "Jörg <joerg@example.com>"},
		{in: Address{}, want: ""},
	}

	for _, tt := range tests {
		if got := FormatAddress(tt.in); got != tt.want {
			t.Fatalf("FormatAddress(%#v) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.Contains(FormatAddress(tt.in), "=?") {
			t.Fatalf("FormatAddress(%#v) used RFC 2047 encoding", tt.in)
		}
	}
}

func TestFormatAddressList(t *testing.T) {
	in := []Address{
		{Name: "Alice", Email: "alice@example.com"},
		{},
		{Name: "Bob"},
		{Email: "carol@example.com"},
	}
	want := []string{"Alice <alice@example.com>", "Bob", "carol@example.com"}
	if got := FormatAddressList(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("FormatAddressList() = %#v, want %#v", got, want)
	}
	if got := FormatAddressList(nil); got == nil || len(got) != 0 {
		t.Fatalf("FormatAddressList(nil) = %#v", got)
	}
}
