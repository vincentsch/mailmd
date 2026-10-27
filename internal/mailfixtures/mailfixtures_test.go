package mailfixtures_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/mailfixtures"
)

func TestMessagesAreIndependent(t *testing.T) {
	first := mailfixtures.Messages()
	second := mailfixtures.Messages()
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("Messages returned no seeds")
	}

	// Change each slice and map inside the first result. If a later change makes
	// two calls share a slice or a map, the second result changes and this test
	// fails.
	first[0].Subject = "mutated"
	first[0].Folders = append(first[0].Folders, "Mutated")
	first[0].References = append(first[0].References, "<mutated@example.com>")
	first[0].To = append(first[0].To, mail.Address{Email: "mutated@example.com"})
	first[0].Attachments[0].Data[0] = 'x'
	first[0].RawHeaders["X-Trace"][0] = "mutated"
	first[0].RawHeaders["X-New"] = []string{"mutated"}

	if got := second[0].Subject; got != "Quarterly Plan" {
		t.Fatalf("second Subject = %q, want original", got)
	}
	if got := len(second[0].Folders); got != 1 {
		t.Fatalf("second Folders len = %d, want 1", got)
	}
	if got := len(second[0].References); got != 4 {
		t.Fatalf("second References len = %d, want 4", got)
	}
	if got := len(second[0].To); got != 2 {
		t.Fatalf("second To len = %d, want 2", got)
	}
	if got := string(second[0].Attachments[0].Data); got != "PDF" {
		t.Fatalf("second attachment data = %q, want PDF", got)
	}
	if got := second[0].RawHeaders["X-Trace"][0]; got != "trace-a" {
		t.Fatalf("second RawHeaders X-Trace[0] = %q, want trace-a", got)
	}
	if _, ok := second[0].RawHeaders["X-New"]; ok {
		t.Fatal("second RawHeaders gained X-New from first mutation")
	}
}

func TestMessagesHaveEmptyLocalIDAndDerivableIdentity(t *testing.T) {
	for _, seed := range mailfixtures.Messages() {
		if seed.LocalID != "" {
			t.Fatalf("%q LocalID = %q, want empty", seed.Subject, seed.LocalID)
		}
		id, err := mail.DeriveLocalID(seed)
		if err != nil {
			t.Fatalf("%q DeriveLocalID: %v", seed.Subject, err)
		}
		short := mail.ShortID(id)
		if len(short) != 16 || strings.ToLower(short) != short || strings.Trim(short, "0123456789abcdef") != "" {
			t.Fatalf("%q ShortID = %q, want 16 lowercase hex chars", seed.Subject, short)
		}
	}
}

func TestMessagesCoverage(t *testing.T) {
	msgs := mailfixtures.Messages()
	if got, want := len(msgs), 11; got != want {
		t.Fatalf("Messages len = %d, want %d", got, want)
	}

	// Other tests rely on the fixtures having each of these kinds of messages.
	// This test fails when no fixture has one of them any more. It doesn't test
	// the writer.
	var plainTextOnly, htmlOnly, hostileHTML, payload, payloadless bool
	var inlineCID, multiFolder, zeroFolder, missingMessageID bool
	var missingDate, emptySubject, yamlHostile, unsafeTokens bool
	var duplicateAttachmentNames, payloadGap bool
	twinIDs := map[string]string{}
	twinDates := map[time.Time]struct{}{}

	for _, m := range msgs {
		if m.BodyText != "" && m.BodyHTML == "" {
			plainTextOnly = true
		}
		if m.BodyText == "" && m.BodyHTML != "" {
			htmlOnly = true
		}
		if strings.Contains(m.BodyHTML, "<script") &&
			strings.Contains(m.BodyHTML, "<style") &&
			strings.Contains(m.BodyHTML, `width="1"`) &&
			strings.Contains(m.BodyHTML, "javascript:") {
			hostileHTML = true
		}
		if len(diskFolders(m.Folders)) >= 2 {
			multiFolder = true
		}
		if len(m.Folders) == 0 {
			zeroFolder = true
		}
		if m.MessageID == "" {
			missingMessageID = true
		}
		if m.Date.IsZero() {
			missingDate = true
		}
		if m.Subject == "" {
			emptySubject = true
		}
		if containsAll(m.Subject, `"`, ":", "\n", ">") && containsAll(m.From.Name, `"`, ":", "\n", ">") {
			yamlHostile = true
		}
		if containsAll(strings.Join(m.Folders, "\n"), ".mailmd", ".git", "..", "CON") {
			unsafeTokens = true
		}
		seenNames := map[string]struct{}{}
		for _, att := range m.Attachments {
			if att.Data != nil {
				payload = true
			} else {
				payloadless = true
			}
			if att.Inline && att.ContentID != "" {
				inlineCID = true
			}
			if _, ok := seenNames[att.Filename]; ok {
				duplicateAttachmentNames = true
			}
			seenNames[att.Filename] = struct{}{}
		}
		if len(m.Attachments) >= 3 && m.Attachments[0].Data != nil && m.Attachments[1].Data == nil && m.Attachments[2].Data != nil {
			payloadGap = true
		}
		if !m.Date.IsZero() && m.Subject == "Status Update" {
			id, err := mail.DeriveLocalID(m)
			if err != nil {
				t.Fatalf("DeriveLocalID(%q): %v", m.From.Email, err)
			}
			twinIDs[m.From.Email] = id
			twinDates[m.Date] = struct{}{}
		}
	}

	checks := map[string]bool{
		"plain text only":             plainTextOnly,
		"html only":                   htmlOnly,
		"hostile html":                hostileHTML,
		"payload attachment":          payload,
		"payload-less attachment":     payloadless,
		"inline content-id":           inlineCID,
		"multi-folder":                multiFolder,
		"zero-folder":                 zeroFolder,
		"missing message-id":          missingMessageID,
		"missing date":                missingDate,
		"empty subject":               emptySubject,
		"yaml hostile text":           yamlHostile,
		"unsafe path tokens":          unsafeTokens,
		"duplicate attachment names":  duplicateAttachmentNames,
		"full-list attachment gap":    payloadGap,
		"same-date subject pair size": len(twinIDs) == 2,
		"same-date subject pair date": len(twinDates) == 1,
	}
	for name, ok := range checks {
		if !ok {
			t.Fatalf("fixture coverage missing %s", name)
		}
	}
	if twinIDs["alice@example.com"] == "" || twinIDs["alice@example.com"] == twinIDs["bob@example.com"] {
		t.Fatalf("same-date same-subject twins did not derive distinct ids: %#v", twinIDs)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

func diskFolders(raw []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, folder := range raw {
		if folder == "" {
			continue
		}
		parts := mail.NormalizeFolderPath(folder)
		if len(parts) == 0 {
			out["Unfiled"] = struct{}{}
			continue
		}
		out[strings.Join(parts, "/")] = struct{}{}
	}
	return out
}
