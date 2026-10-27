package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	mdsync "github.com/vincentsch/mailmd/internal/sync"
)

func TestStatusNamesEachThingOnce(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s := AccountStatus{
		StatusResult: mdsync.StatusResult{
			Account: "work", Archive: "/home/dana/mail/work", Synced: true, Messages: 3,
			Diagnostics: map[backend.DiagnosticReason]int{backend.ReasonTooLarge: 1, backend.ReasonParseFailure: 2},
		},
		Provider: "IMAP",
		LastSync: &accounts.LastSync{Finished: now, Outcome: mdsync.OutcomeInterrupted, New: 2, Error: "The sync was interrupted."},
	}
	var out bytes.Buffer
	renderStatus(&out, StatusOutput{Accounts: []AccountStatus{s}}, "/home/dana", now, false)
	text := out.String()
	if strings.Contains(text, "The sync was interrupted.") || !strings.Contains(text, "Last sync  just now, interrupted, 2 new") {
		t.Fatalf("the interruption is not named once:\n%s", text)
	}
	if strings.Count(text, "Problems") != 1 || !strings.Contains(text, "1 message too large") || !strings.Contains(text, "2 messages could not be read") {
		t.Fatalf("problems:\n%s", text)
	}
}
