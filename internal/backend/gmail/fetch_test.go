package gmail

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

func TestFetchRetryableFailureDoesNotPoisonBackend(t *testing.T) {
	fake := newFakeClient()
	fake.messageErrors[""] = apiErr(http.StatusTooManyRequests, "rateLimitExceeded")
	b := openFakeBackend(t, fake)

	if res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey}); !errors.Is(err, mdbackend.ErrRetryable) || !reflect.DeepEqual(res, mdbackend.FetchResult{}) {
		t.Fatalf("first Fetch = result %#v error %v, want retryable zero result", res, err)
	}
	delete(fake.messageErrors, "")
	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey})
	if err != nil {
		t.Fatalf("second Fetch after retryable: %v", err)
	}
	if !res.Done || len(res.Cursor) == 0 {
		t.Fatalf("second result = %#v, want done with cursor", res)
	}
}

func TestFetchFullEnumerationOrdersMessagesAndReturnsReadyCursor(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}, {ID: "TRASH", Name: "Trash", Type: "system"}}
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "b"}, {ID: "a"}}}
	fake.messages["a"] = gmailMessage{ID: "a", ThreadID: "ta", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("a", "Subject A", "body a")}
	fake.messages["b"] = gmailMessage{ID: "b", ThreadID: "tb", InternalDate: 200, LabelIDs: []string{"TRASH"}, Raw: rawRFC822("b", "Subject B", "body b")}
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Done || len(res.Cursor) == 0 {
		t.Fatalf("result done/cursor = %v/%q", res.Done, res.Cursor)
	}
	if got, want := fetchSubjects(res), []string{"Subject A", "Subject B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subjects = %#v, want %#v", got, want)
	}
	if got, want := res.Messages[0].Folders, []string{"Inbox"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("message a folders = %#v, want %#v", got, want)
	}
	if got, want := res.Messages[1].Folders, []string{"Trash"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("message b folders = %#v, want %#v", got, want)
	}
	if len(fake.listCalls) != 1 || !fake.listCalls[0].IncludeSpamTrash || fake.listCalls[0].MaxResults > maxProviderPageSize {
		t.Fatalf("list calls = %#v", fake.listCalls)
	}
	state, err := decodeCursor(res.Cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if state.Mode != modeReady || state.HistoryID != "10" {
		t.Fatalf("cursor state = %#v", state)
	}
}

func TestFetchEnumerationPartialResumePendingAndPageToken(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.messagePages[""] = gmailMessagePage{
		Messages:      []gmailMessageRef{{ID: "b"}, {ID: "a"}},
		NextPageToken: "p2",
	}
	fake.messagePages["p2"] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "c"}}}
	fake.messages["a"] = gmailMessage{ID: "a", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("a", "Subject A", "body a")}
	fake.messages["b"] = gmailMessage{ID: "b", InternalDate: 200, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("b", "Subject B", "body b")}
	fake.messages["c"] = gmailMessage{ID: "c", InternalDate: 300, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("c", "Subject C", "body c")}
	b := openFakeBackend(t, fake)

	first, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Limit: 1})
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.Done || !reflect.DeepEqual(fetchSubjects(first), []string{"Subject A"}) {
		t.Fatalf("first result = done %v subjects %#v", first.Done, fetchSubjects(first))
	}
	state, _ := decodeCursor(first.Cursor)
	if state.Mode != modeEnumerate || state.PageToken != "p2" || !reflect.DeepEqual(state.PendingIDs, []string{"b"}) {
		t.Fatalf("first cursor = %#v", state)
	}

	second, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: first.Cursor, Limit: 1})
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second.Done || !reflect.DeepEqual(fetchSubjects(second), []string{"Subject B"}) {
		t.Fatalf("second result = done %v subjects %#v", second.Done, fetchSubjects(second))
	}
	state, _ = decodeCursor(second.Cursor)
	if state.Mode != modeEnumerate || state.PageToken != "p2" || len(state.PendingIDs) != 0 {
		t.Fatalf("second cursor = %#v", state)
	}

	third, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: second.Cursor, Limit: 10})
	if err != nil {
		t.Fatalf("third Fetch: %v", err)
	}
	if !third.Done || !reflect.DeepEqual(fetchSubjects(third), []string{"Subject C"}) {
		t.Fatalf("third result = done %v subjects %#v", third.Done, fetchSubjects(third))
	}
}

func TestFetchEnumerationInvalidPageTokenRestartsWithFreshTarget(t *testing.T) {
	fake := newFakeClient()
	fake.profiles = []gmailProfile{
		{EmailAddress: "User@Example.COM", HistoryID: "20"},
		{EmailAddress: "User@Example.COM", HistoryID: "30"},
	}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.messageErrors["bad"] = apiErr(http.StatusBadRequest, "invalidPageToken")
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "a"}}}
	fake.messages["a"] = gmailMessage{ID: "a", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("a", "Subject A", "body a")}
	fp, _ := accountFingerprint(gmailProfile{EmailAddress: "User@Example.COM"})
	cursor := encodeCursor(cursorState{Mode: modeEnumerate, Fingerprint: fp, TargetHistoryID: "10", PageToken: "bad"})
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: cursor, Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	state, _ := decodeCursor(res.Cursor)
	if !res.Done || state.Mode != modeReady || state.HistoryID != "30" {
		t.Fatalf("result/cursor = done %v state %#v", res.Done, state)
	}
	if got := strings.Join(fake.calls, ","); !strings.Contains(got, "listMessages:bad") || !strings.Contains(got, "listMessages:") {
		t.Fatalf("calls did not restart enumeration: %s", got)
	}
}

func TestFetchEmptyAccountEnumerationReturnsReadyCursor(t *testing.T) {
	fake := newFakeClient()
	b := openFakeBackend(t, fake)
	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Done || len(res.Messages) != 0 || len(res.Diagnostics) != 0 || len(res.Cursor) == 0 {
		t.Fatalf("empty account result = %#v", res)
	}
}

func TestFetchReadyHistoryNoChange(t *testing.T) {
	fake := newFakeClient()
	fp, _ := accountFingerprint(fake.profile)
	cursor := encodeReadyCursor(fp, "10")
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: cursor})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Done || len(res.Messages) != 0 || len(res.Diagnostics) != 0 {
		t.Fatalf("no-change history result = %#v", res)
	}
	if got, want := strings.Join(fake.calls, ","), "profile,labels"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestFetchHistoryPartialDoesNotCommitUntilDrained(t *testing.T) {
	fake := newFakeClient()
	fake.profile = gmailProfile{EmailAddress: "User@Example.COM", HistoryID: "20"}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.historyPages[historyKey("10", "")] = gmailHistoryPage{History: []gmailHistory{{
		ID:              "11",
		MessagesAdded:   []gmailMessageRef{{ID: "m2"}, {ID: "m1"}},
		LabelsAdded:     []gmailMessageRef{{ID: "m1"}},
		LabelsRemoved:   []gmailMessageRef{{ID: "m1"}},
		MessagesDeleted: []gmailMessageRef{{ID: "gone"}},
	}}}
	fake.messages["m1"] = gmailMessage{ID: "m1", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("m1", "Subject M1", "body m1")}
	fake.messages["m2"] = gmailMessage{ID: "m2", InternalDate: 200, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("m2", "Subject M2", "body m2")}
	fp, _ := accountFingerprint(fake.profile)
	cursor := encodeReadyCursor(fp, "10")
	b := openFakeBackend(t, fake)

	first, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: cursor, Limit: 1})
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}

	if first.Done || len(first.Messages) != 0 || len(first.Diagnostics) != 1 {
		t.Fatalf("first bounded prefix = %#v", first)
	}
	if got, want := diagnosticRefs(first.Diagnostics), []string{"gone"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first diagnostic = %v", got)
	}
	state, _ := decodeCursor(first.Cursor)
	if state.Mode != modeHistory || state.BaseHistoryID != "10" || state.TargetHistoryID != "20" || !reflect.DeepEqual(state.PendingIDs, []string{"m1", "m2"}) {
		t.Fatalf("first cursor = %#v", state)
	}
	second, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: first.Cursor, Limit: 1})
	if err != nil || second.Done || !reflect.DeepEqual(fetchSubjects(second), []string{"Subject M1"}) || len(second.Diagnostics) != 0 {
		t.Fatalf("second bounded prefix = %#v %v", second, err)
	}
	state, _ = decodeCursor(second.Cursor)
	if state.Mode != modeHistory || !reflect.DeepEqual(state.PendingIDs, []string{"m2"}) {
		t.Fatalf("second cursor = %#v", state)
	}
	third, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: second.Cursor, Limit: 1})
	if err != nil || !third.Done || !reflect.DeepEqual(fetchSubjects(third), []string{"Subject M2"}) || len(third.Diagnostics) != 0 {
		t.Fatalf("third prefix = %#v %v", third, err)
	}
	state, _ = decodeCursor(third.Cursor)
	if state.Mode != modeReady || state.HistoryID != "20" {
		t.Fatalf("ready cursor = %#v", state)
	}
}

func TestFetchHistoryPartialPageTokenDoesNotCommitUntilDrained(t *testing.T) {
	fake := newFakeClient()
	fake.profile = gmailProfile{EmailAddress: "User@Example.COM", HistoryID: "30"}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.historyPages[historyKey("10", "")] = gmailHistoryPage{
		History:       []gmailHistory{{ID: "11", MessagesAdded: []gmailMessageRef{{ID: "m1"}}}},
		NextPageToken: "p2",
	}
	fake.historyPages[historyKey("10", "p2")] = gmailHistoryPage{
		History: []gmailHistory{{ID: "12", LabelsAdded: []gmailMessageRef{{ID: "m2"}}}},
	}
	fake.messages["m1"] = gmailMessage{ID: "m1", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("m1", "Subject M1", "body m1")}
	fake.messages["m2"] = gmailMessage{ID: "m2", InternalDate: 200, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("m2", "Subject M2", "body m2")}
	fp, _ := accountFingerprint(fake.profile)
	b := openFakeBackend(t, fake)

	first, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: encodeReadyCursor(fp, "10"), Limit: 1})
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.Done || !reflect.DeepEqual(fetchSubjects(first), []string{"Subject M1"}) {
		t.Fatalf("first result = done %v subjects %#v", first.Done, fetchSubjects(first))
	}
	state, _ := decodeCursor(first.Cursor)
	if state.Mode != modeHistory || state.BaseHistoryID != "10" || state.TargetHistoryID != "30" || state.PageToken != "p2" || len(state.PendingIDs) != 0 {
		t.Fatalf("first cursor = %#v", state)
	}

	second, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: first.Cursor, Limit: 10})
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.Done || !reflect.DeepEqual(fetchSubjects(second), []string{"Subject M2"}) {
		t.Fatalf("second result = done %v subjects %#v", second.Done, fetchSubjects(second))
	}
	state, _ = decodeCursor(second.Cursor)
	if state.Mode != modeReady || state.HistoryID != "30" {
		t.Fatalf("second cursor = %#v", state)
	}
}

func TestFetchHistoryDeletionOnlyPagesAreResumable(t *testing.T) {
	fake := newFakeClient()
	fake.profile = gmailProfile{EmailAddress: "User@Example.COM", HistoryID: "40"}
	fake.historyPages[historyKey("10", "")] = gmailHistoryPage{
		History:       []gmailHistory{{ID: "11", MessagesDeleted: []gmailMessageRef{{ID: "gone-1"}}}},
		NextPageToken: "p2",
	}
	fake.historyPages[historyKey("10", "p2")] = gmailHistoryPage{
		History:       []gmailHistory{{ID: "12", MessagesDeleted: []gmailMessageRef{{ID: "gone-2"}}}},
		NextPageToken: "p3",
	}
	fp, _ := accountFingerprint(fake.profile)
	b := openFakeBackend(t, fake)

	first, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: encodeReadyCursor(fp, "10"), Limit: 10})
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.Done || !reflect.DeepEqual(diagnosticRefs(first.Diagnostics), []string{"gone-1"}) {
		t.Fatalf("first result = done %v diagnostics %#v", first.Done, first.Diagnostics)
	}
	state, _ := decodeCursor(first.Cursor)
	if state.Mode != modeHistory || state.BaseHistoryID != "10" || state.TargetHistoryID != "40" || state.PageToken != "p2" || len(state.PendingIDs) != 0 {
		t.Fatalf("first cursor = %#v", state)
	}
	if got, want := fake.historyCalls, []listHistoryCall{{StartHistoryID: "10", PageToken: "", MaxResults: maxProviderPageSize}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first history calls = %#v, want %#v", got, want)
	}

	second, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: first.Cursor, Limit: 10})
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second.Done || !reflect.DeepEqual(diagnosticRefs(second.Diagnostics), []string{"gone-2"}) {
		t.Fatalf("second result = done %v diagnostics %#v", second.Done, second.Diagnostics)
	}
	state, _ = decodeCursor(second.Cursor)
	if state.Mode != modeHistory || state.PageToken != "p3" {
		t.Fatalf("second cursor = %#v", state)
	}
}

func TestFetchHistoryStaleFallsBackToFullEnumeration(t *testing.T) {
	fake := newFakeClient()
	fake.profiles = []gmailProfile{
		{EmailAddress: "User@Example.COM", HistoryID: "20"},
		{EmailAddress: "User@Example.COM", HistoryID: "30"},
	}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.historyErrors[historyKey("10", "")] = apiErr(http.StatusNotFound, "notFound")
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "a"}}}
	fake.messages["a"] = gmailMessage{ID: "a", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("a", "Subject A", "body a")}
	fp, _ := accountFingerprint(gmailProfile{EmailAddress: "User@Example.COM"})
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: encodeReadyCursor(fp, "10")})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	state, _ := decodeCursor(res.Cursor)
	if !res.Done || state.Mode != modeReady || state.HistoryID != "30" || !reflect.DeepEqual(fetchSubjects(res), []string{"Subject A"}) {
		t.Fatalf("fallback result = done %v state %#v subjects %#v", res.Done, state, fetchSubjects(res))
	}
}

func TestFetchHistoryInvalidPageTokenRestartsFromBase(t *testing.T) {
	fake := newFakeClient()
	fake.profile = gmailProfile{EmailAddress: "User@Example.COM", HistoryID: "20"}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.historyErrors[historyKey("10", "bad")] = apiErr(http.StatusBadRequest, "invalidPageToken")
	fake.historyPages[historyKey("10", "")] = gmailHistoryPage{History: []gmailHistory{{ID: "12", MessagesAdded: []gmailMessageRef{{ID: "m"}}}}}
	fake.messages["m"] = gmailMessage{ID: "m", InternalDate: 100, LabelIDs: []string{"INBOX"}, Raw: rawRFC822("m", "Subject M", "body")}
	fp, _ := accountFingerprint(fake.profile)
	b := openFakeBackend(t, fake)
	cursor := encodeCursor(cursorState{Mode: modeHistory, Fingerprint: fp, BaseHistoryID: "10", TargetHistoryID: "20", PageToken: "bad"})

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: cursor})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.Done || !reflect.DeepEqual(fetchSubjects(res), []string{"Subject M"}) {
		t.Fatalf("history restart result = done %v subjects %#v", res.Done, fetchSubjects(res))
	}
	if got, want := fake.historyCalls, []listHistoryCall{
		{StartHistoryID: "10", PageToken: "bad", MaxResults: maxProviderPageSize},
		{StartHistoryID: "10", PageToken: "", MaxResults: maxProviderPageSize},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history calls = %#v, want %#v", got, want)
	}
}

func TestFetchRestartEnumerationRejectsChangedAccountProfile(t *testing.T) {
	fake := newFakeClient()
	fake.profiles = []gmailProfile{
		{EmailAddress: "User@Example.COM", HistoryID: "20"},
		{EmailAddress: "other@example.com", HistoryID: "30"},
	}
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	fake.messageErrors["bad"] = apiErr(http.StatusBadRequest, "invalidPageToken")
	fp, _ := accountFingerprint(gmailProfile{EmailAddress: "User@Example.COM"})
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{
		ScopeKey: accountScopeKey,
		Cursor:   encodeCursor(cursorState{Mode: modeEnumerate, Fingerprint: fp, TargetHistoryID: "10", PageToken: "bad"}),
	})
	if !errors.Is(err, mdbackend.ErrInvalidCursor) || !reflect.DeepEqual(res, mdbackend.FetchResult{}) {
		t.Fatalf("Fetch changed-account restart = result %#v error %v", res, err)
	}
	if got, want := strings.Join(fake.calls, ","), "profile,labels,listMessages:bad,profile"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestFetchDiagnosticsForParseIdentityAndVanishedMessages(t *testing.T) {
	fake := newFakeClient()
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "badraw"}, {ID: "identity"}, {ID: "gone"}}}
	fake.messages["badraw"] = gmailMessage{ID: "badraw", InternalDate: 200, Raw: "not base64"}
	fake.messages["identity"] = gmailMessage{ID: "identity", InternalDate: 100, Raw: identitylessRFC822()}
	fake.getErrors["gone"] = apiErr(http.StatusNotFound)
	b := openFakeBackend(t, fake)

	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []mdbackend.Diagnostic{
		{ScopeKey: accountScopeKey, Ref: "gone", Reason: mdbackend.ReasonVanished, Backend: "gmail"},
		{ScopeKey: accountScopeKey, Ref: "identity", Reason: mdbackend.ReasonIdentityFailure, Backend: "gmail"},
		{ScopeKey: accountScopeKey, Ref: "badraw", Reason: mdbackend.ReasonParseFailure, Backend: "gmail"},
	}
	if !reflect.DeepEqual(res.Diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", res.Diagnostics, want)
	}
	for _, diag := range res.Diagnostics {
		text := diag.Ref + string(diag.Reason) + diag.Backend + diag.ScopeKey
		for _, forbidden := range []string{"secret-token", "raw MIME", "body a", "attachment bytes"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("diagnostic leaked %q: %#v", forbidden, diag)
			}
		}
	}
}

func TestFetchMissingScopeAndCancellation(t *testing.T) {
	fake := newFakeClient()
	b := openFakeBackend(t, fake)
	if _, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: "other"}); !errors.Is(err, mdbackend.ErrScopeNotFound) {
		t.Fatalf("missing scope error = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("missing scope made provider calls: %#v", fake.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := b.Fetch(ctx, mdbackend.FetchRequest{ScopeKey: accountScopeKey})
	if err != context.Canceled || !reflect.DeepEqual(res, mdbackend.FetchResult{}) {
		t.Fatalf("canceled fetch = result %#v error %v", res, err)
	}
}

func fetchSubjects(res mdbackend.FetchResult) []string {
	out := make([]string, len(res.Messages))
	for i := range res.Messages {
		out[i] = res.Messages[i].Subject
	}
	return out
}

func diagnosticRefs(diags []mdbackend.Diagnostic) []string {
	out := make([]string, len(diags))
	for i := range diags {
		out[i] = diags[i].Ref
	}
	return out
}

func TestSinceAndSpamAreSentToGmail(t *testing.T) {
	fake := newFakeClient()
	b := openFakeBackend(t, fake)
	_, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Since: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC), SkipJunk: true})
	if err != nil {
		t.Fatal(err)
	}
	// Gmail gets seconds since 1970, because it reads a date as midnight in
	// California.
	if len(fake.listCalls) == 0 || fake.listCalls[0].Query != "after:1738281599" || fake.listCalls[0].IncludeSpamTrash {
		t.Fatalf("list calls = %#v", fake.listCalls)
	}
}

func TestGmailMessagesKeepTheReceivedDateAndCanBeFetchedAgain(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{{ID: "INBOX", Name: "Inbox", Type: "system"}}
	// The message has no Date header, so mailmd uses the time Gmail received
	// it.
	raw := "Message-ID: <nodate@example.test>\r\nFrom: a@example.test\r\nSubject: No date\r\n\r\nbody\r\n"
	fake.messages["m1"] = gmailMessage{ID: "m1", LabelIDs: []string{"INBOX"}, Raw: rawBase64(raw), InternalDate: 1772600767000}
	fake.messagePages[""] = gmailMessagePage{Messages: []gmailMessageRef{{ID: "m1"}}}
	b := openFakeBackend(t, fake)
	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey})
	if err != nil || len(res.Messages) != 1 {
		t.Fatalf("fetch = %+v, %v", res, err)
	}
	if got := res.Messages[0].Received; !got.Equal(time.UnixMilli(1772600767000)) {
		t.Fatalf("received = %v", got)
	}
	again, err := b.FetchRefs(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey}, []string{"m1", "gone"})
	if err != nil || len(again.Messages) != 1 || again.Messages[0].Ref != "m1" || len(again.Diagnostics) != 1 || again.Diagnostics[0].Reason != mdbackend.ReasonVanished {
		t.Fatalf("FetchRefs = %+v, %v", again, err)
	}
	folders, err := b.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var archive bool
	for _, f := range folders {
		archive = archive || (f.Path == "Archive" && f.Role == mdbackend.RoleArchive)
	}
	if !archive {
		t.Fatalf("folders = %+v, want the Archive folder for mail without labels", folders)
	}
}
