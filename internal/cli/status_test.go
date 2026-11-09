package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap/imaptest"
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

// When no message has a read or answered mark, the first sync and status
// say so. Without that, an empty flags field looks like unread mail.
func TestNoReadMarksAreNamedOnce(t *testing.T) {
	server := imaptest.Start(t, imaptest.Options{Folders: officeFolders()})
	root, home := t.TempDir(), t.TempDir()
	opts := Options{ConfigRoot: root, HomeDir: home, RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "work", "--password-stdin")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	note := "None of these messages is marked as read or answered. Either they are all unread, or the server doesn't keep these marks."
	if _, stdout, _ := invoke(t, opts, "", "sync"); !strings.Contains(stdout, note) {
		t.Fatalf("first sync:\n%s", stdout)
	}
	if _, stdout, _ := invoke(t, opts, "", "sync"); strings.Contains(stdout, "marked as read") {
		t.Fatalf("second sync:\n%s", stdout)
	}
	_, stdout, _ := invoke(t, opts, "", "status", "work")
	if !strings.Contains(stdout, "Flags      none of the 4 messages is marked as read or answered") || !strings.Contains(stdout, "Either they are all unread, or the server doesn't keep these marks.") {
		t.Fatalf("status:\n%s", stdout)
	}
	_, stdout, _ = invoke(t, opts, "", "status", "--json")
	var out StatusOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || !out.Accounts[0].NoReadMarks {
		t.Fatalf("status --json %v:\n%s", err, stdout)
	}

	// The default test mailbox has a message that was read.
	server = imaptest.Start(t, imaptest.Options{})
	opts = Options{ConfigRoot: t.TempDir(), HomeDir: t.TempDir(), RootCAs: server.CertPool()}
	if code, stdout, stderr := invoke(t, opts, server.Password()+"\n", imapArgs(server, "--account", "other", "--password-stdin", "--folder", "INBOX")...); code != 0 {
		t.Fatalf("connect %d %s %s", code, stdout, stderr)
	}
	_, synced, _ := invoke(t, opts, "", "sync")
	_, status, _ := invoke(t, opts, "", "status", "other")
	if strings.Contains(synced+status, "marked as read") {
		t.Fatalf("a mailbox with read mail:\n%s\n%s", synced, status)
	}
}
