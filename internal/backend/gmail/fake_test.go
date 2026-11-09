package gmail

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
)

type fakeGmailClient struct {
	profile    gmailProfile
	profiles   []gmailProfile
	profileErr error

	labels    []gmailLabel
	labelsErr error
	// totals holds the message count of each label, by its ID.
	totals map[string]int

	messagePages  map[string]gmailMessagePage
	messageErrors map[string]error
	listCalls     []listMessagesCall

	messages  map[string]gmailMessage
	getErrors map[string]error

	historyPages  map[string]gmailHistoryPage
	historyErrors map[string]error
	historyCalls  []listHistoryCall

	calls []string
}

type listMessagesCall struct {
	PageToken        string
	IncludeSpamTrash bool
	Query            string
	MaxResults       int64
}

type listHistoryCall struct {
	StartHistoryID string
	PageToken      string
	MaxResults     int64
}

func newFakeClient() *fakeGmailClient {
	return &fakeGmailClient{
		profile:       gmailProfile{EmailAddress: "User@Example.COM", HistoryID: "10"},
		messagePages:  map[string]gmailMessagePage{},
		messageErrors: map[string]error{},
		messages:      map[string]gmailMessage{},
		getErrors:     map[string]error{},
		historyPages:  map[string]gmailHistoryPage{},
		historyErrors: map[string]error{},
	}
}

func (f *fakeGmailClient) Profile(ctx context.Context, userID string) (gmailProfile, error) {
	_ = userID
	f.calls = append(f.calls, "profile")
	if err := ctx.Err(); err != nil {
		return gmailProfile{}, err
	}
	if f.profileErr != nil {
		return gmailProfile{}, f.profileErr
	}
	if len(f.profiles) > 0 {
		profile := f.profiles[0]
		f.profiles = f.profiles[1:]
		return profile, nil
	}
	return f.profile, nil
}

func (f *fakeGmailClient) Labels(ctx context.Context, userID string) ([]gmailLabel, error) {
	_ = userID
	f.calls = append(f.calls, "labels")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.labelsErr != nil {
		return nil, f.labelsErr
	}
	return append([]gmailLabel(nil), f.labels...), nil
}

func (f *fakeGmailClient) LabelTotal(ctx context.Context, userID, id string) (int, error) {
	_ = userID
	f.calls = append(f.calls, "label:"+id)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, ok := f.totals[id]
	if !ok {
		return 0, apiErr(http.StatusNotFound)
	}
	return n, nil
}

func (f *fakeGmailClient) ListMessages(ctx context.Context, userID, pageToken string, includeSpamTrash bool, query string, maxResults int64) (gmailMessagePage, error) {
	_ = userID
	f.calls = append(f.calls, "listMessages:"+pageToken)
	f.listCalls = append(f.listCalls, listMessagesCall{PageToken: pageToken, IncludeSpamTrash: includeSpamTrash, Query: query, MaxResults: maxResults})
	if err := ctx.Err(); err != nil {
		return gmailMessagePage{}, err
	}
	if err := f.messageErrors[pageToken]; err != nil {
		return gmailMessagePage{}, err
	}
	return f.messagePages[pageToken], nil
}

func (f *fakeGmailClient) GetMessage(ctx context.Context, userID, id string) (gmailMessage, error) {
	_ = userID
	f.calls = append(f.calls, "get:"+id)
	if err := ctx.Err(); err != nil {
		return gmailMessage{}, err
	}
	if err := f.getErrors[id]; err != nil {
		return gmailMessage{}, err
	}
	msg, ok := f.messages[id]
	if !ok {
		return gmailMessage{}, apiErr(http.StatusNotFound)
	}
	return msg, nil
}

// MessageHeaders returns the header lines of a stored message.
func (f *fakeGmailClient) MessageHeaders(ctx context.Context, userID, id string) (gmailMessage, []gmailHeader, error) {
	f.calls = append(f.calls, "headers:"+id)
	msg, ok := f.messages[id]
	if !ok {
		return gmailMessage{}, nil, apiErr(http.StatusNotFound)
	}
	raw, _ := decodeRawMessage(msg.Raw)
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	var headers []gmailHeader
	for _, line := range strings.Split(head, "\r\n") {
		if name, value, ok := strings.Cut(line, ": "); ok {
			headers = append(headers, gmailHeader{Name: name, Value: value})
		}
	}
	return msg, headers, nil
}

func (f *fakeGmailClient) ListHistory(ctx context.Context, userID, startHistoryID, pageToken string, maxResults int64) (gmailHistoryPage, error) {
	_ = userID
	f.calls = append(f.calls, "history:"+startHistoryID+":"+pageToken)
	f.historyCalls = append(f.historyCalls, listHistoryCall{StartHistoryID: startHistoryID, PageToken: pageToken, MaxResults: maxResults})
	if err := ctx.Err(); err != nil {
		return gmailHistoryPage{}, err
	}
	key := historyKey(startHistoryID, pageToken)
	if err := f.historyErrors[key]; err != nil {
		return gmailHistoryPage{}, err
	}
	return f.historyPages[key], nil
}

func historyKey(startHistoryID, pageToken string) string {
	return startHistoryID + "\x00" + pageToken
}

func openFakeBackend(t *testing.T, f *fakeGmailClient) *Backend {
	t.Helper()
	b, err := Open(context.Background(), Config{client: f})
	if err != nil {
		t.Fatalf("Open fake backend: %v", err)
	}
	return b
}

func apiErr(code int, reasons ...string) error {
	items := make([]googleapi.ErrorItem, 0, len(reasons))
	for _, reason := range reasons {
		items = append(items, googleapi.ErrorItem{Reason: reason, Message: "message-redacted"})
	}
	return &googleapi.Error{
		Code:   code,
		Header: http.Header{},
		Body:   "secret-token raw MIME body attachment bytes",
		Errors: items,
	}
}

func apiErrWithRetryAfter(code int, retryAfter string, reasons ...string) error {
	err := apiErr(code, reasons...).(*googleapi.Error)
	err.Header.Set("Retry-After", retryAfter)
	return err
}

func rawRFC822(id, subject, body string) string {
	return rawBase64(`Message-ID: <` + id + `@example.test>
Date: Fri, 03 Jul 2026 08:00:00 +0000
From: Sender <sender@example.test>
To: Recipient <recipient@example.test>
Subject: ` + subject + `
Content-Type: text/plain; charset=utf-8

` + body + `
`)
}

func htmlRFC822(id, subject string) string {
	return rawBase64(`Message-ID: <` + id + `@example.test>
Date: Fri, 03 Jul 2026 08:00:00 +0000
From: Sender <sender@example.test>
To: Recipient <recipient@example.test>
Subject: ` + subject + `
Content-Type: text/html; charset=utf-8

<p>Hello <strong>HTML</strong>.</p>
`)
}

func attachmentRFC822(id, subject string) string {
	return rawBase64(`Message-ID: <` + id + `@example.test>
Date: Fri, 03 Jul 2026 08:00:00 +0000
From: Sender <sender@example.test>
To: Recipient <recipient@example.test>
Subject: ` + subject + `
Content-Type: multipart/mixed; boundary="mixed"

--mixed
Content-Type: text/plain; charset=utf-8

Body with attachment.
--mixed
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="notes.txt"

attachment text
--mixed--
`)
}

func identitylessRFC822() string {
	return rawBase64(`Content-Type: text/plain; charset=utf-8

`)
}

func rawBase64(raw string) string {
	raw = strings.TrimPrefix(raw, "\n")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\n", "\r\n")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
