package gmail

import (
	"context"
	"os"
	"reflect"
	"testing"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/writer"
)

func TestGmailFetchWriterCompatibilityMultiLabelHTMLAndAttachments(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{
		{ID: "INBOX", Name: "Inbox", Type: "system"},
		{ID: "SENT", Name: "Sent", Type: "system"},
		{ID: "u-work", Name: "Work", Type: "user"},
	}
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "multi"}, {ID: "html"}, {ID: "att"}}}
	fake.messages["multi"] = gmailMessage{ID: "multi", InternalDate: 100, LabelIDs: []string{"INBOX", "SENT", "u-work"}, Raw: rawRFC822("multi", "Multi Label", "multi body")}
	fake.messages["html"] = gmailMessage{ID: "html", InternalDate: 200, LabelIDs: []string{"INBOX"}, Raw: htmlRFC822("html", "HTML Only")}
	fake.messages["att"] = gmailMessage{ID: "att", InternalDate: 300, LabelIDs: []string{"u-work"}, Raw: attachmentRFC822("att", "Attachment Mail")}
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	bySubject := map[string]int{}
	for i, msg := range res.Messages {
		bySubject[msg.Subject] = i
	}
	root := t.TempDir()
	opts := writer.Options{Root: root, Account: "gmail-account"}
	for _, msg := range res.Messages {
		first, err := writer.WriteMessage(context.Background(), opts, msg)
		if err != nil {
			t.Fatalf("first WriteMessage %q: %v", msg.Subject, err)
		}
		second, err := writer.WriteMessage(context.Background(), opts, msg)
		if err != nil {
			t.Fatalf("second WriteMessage %q: %v", msg.Subject, err)
		}
		if second.Changed {
			t.Fatalf("second WriteMessage %q changed: %#v", msg.Subject, second)
		}
		if first.LocalID != second.LocalID {
			t.Fatalf("local id changed for %q", msg.Subject)
		}
	}

	multi := res.Messages[bySubject["Multi Label"]]
	multiPlan, err := writer.PlanMessage(context.Background(), opts, multi)
	if err != nil {
		t.Fatalf("PlanMessage multi: %v", err)
	}
	if got, want := len(multiPlan.Files), 1; got != want {
		t.Fatalf("a message with several labels has %d files, want %d", got, want)
	}

	html := res.Messages[bySubject["HTML Only"]]
	if html.BodyHTML == "" || html.BodyText != "" {
		t.Fatalf("html message bodies = text %q html %q", html.BodyText, html.BodyHTML)
	}

	att := res.Messages[bySubject["Attachment Mail"]]
	attPlan, err := writer.PlanMessage(context.Background(), opts, att)
	if err != nil {
		t.Fatalf("PlanMessage attachment: %v", err)
	}
	if len(attPlan.Files) != 1 || len(attPlan.Files[0].Attachments) != 1 {
		t.Fatalf("attachment plan = %#v", attPlan)
	}
	sidecar := attPlan.Files[0].Attachments[0].Path
	if sidecar == "" {
		t.Fatalf("attachment sidecar path empty")
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("attachment sidecar not written: %v", err)
	}

	again, err := writer.PlanMessage(context.Background(), opts, att)
	if err != nil {
		t.Fatalf("second PlanMessage attachment: %v", err)
	}
	if !reflect.DeepEqual(attPlan, again) {
		t.Fatalf("attachment plan changed\nfirst %#v\nagain %#v", attPlan, again)
	}
}
