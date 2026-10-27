package backend

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryAfterSaturatingDecimalAndDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, raw := range []string{"9223372036854775807", "18446744073709551616", strings.Repeat("9", 100), "9223372037"} {
		if got := RetryAfter(raw, now); got != time.Duration(math.MaxInt64) {
			t.Fatalf("%q overflowed: %s", raw, got)
		}
	}
	for raw, want := range map[string]time.Duration{"0": 0, " 60 ": time.Minute, "900": 15 * time.Minute, "-1": 0, "1junk": 0, "+1": 0, "": 0, now.Add(time.Minute).Format(http.TimeFormat): time.Minute, now.Add(-time.Second).Format(http.TimeFormat): 0} {
		if got := RetryAfter(raw, now); got != want {
			t.Fatalf("%q => %s want %s", raw, got, want)
		}
	}
}
