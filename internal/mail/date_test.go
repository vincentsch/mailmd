package mail

import (
	"testing"
	"time"
)

func TestNormalizeDate(t *testing.T) {
	var zero time.Time
	if got := NormalizeDate(zero, time.UTC); !got.IsZero() {
		t.Fatalf("NormalizeDate(zero) = %v", got)
	}

	src := time.Date(2026, 6, 29, 20, 13, 42, 0, time.FixedZone("CEST", 2*60*60))
	got := NormalizeDate(src, nil)
	if got.Location() != time.UTC {
		t.Fatalf("NormalizeDate nil loc location = %v", got.Location())
	}
	if want := "2026-06-29T18:13:42Z"; got.Format(time.RFC3339) != want {
		t.Fatalf("NormalizeDate nil loc = %s, want %s", got.Format(time.RFC3339), want)
	}

	loc := time.FixedZone("Archive", -5*60*60)
	got = NormalizeDate(src, loc)
	if got.Location() != loc {
		t.Fatalf("NormalizeDate explicit location = %v", got.Location())
	}
	if !got.Equal(src) {
		t.Fatalf("NormalizeDate changed instant: got %v, src %v", got, src)
	}
}
