package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryCountBound(t *testing.T) {
	for _, count := range []int{25, 26} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/profile"):
					fmt.Fprint(w, `{"emailAddress":"fixture@example.test","historyId":"20"}`)
				case strings.HasSuffix(r.URL.Path, "/labels"):
					fmt.Fprint(w, `{"labels":[{"id":"INBOX","name":"INBOX","type":"system"}]}`)
				case strings.HasSuffix(r.URL.Path, "/history"):
					refs := []any{}
					for i := 0; i < count; i++ {
						refs = append(refs, map[string]any{"message": map[string]string{"id": fmt.Sprintf("dead%02d", i)}})
					}
					json.NewEncoder(w).Encode(map[string]any{"history": []any{map[string]any{"id": "11", "messagesDeleted": refs}}, "historyId": "20"})
				default:
					http.Error(w, "unexpected route", 500)
				}
			}))
			defer srv.Close()
			origin, _ := url.Parse(srv.URL)
			b, e := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: localGmailTransport{origin}}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			fp, _ := accountFingerprint(gmailProfile{EmailAddress: "fixture@example.test", HistoryID: "20"})
			result, e := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: encodeReadyCursor(fp, "10")})
			t.Logf("deletions=%d messages=%d diagnostics=%d done=%t error=%v", count, len(result.Messages), len(result.Diagnostics), result.Done, e)
			if e != nil {
				t.Fatal(e)
			}
			if len(result.Messages)+len(result.Diagnostics) > 25 {
				t.Fatal("default batch exceeds provider work bound; sync rejects it forever as integrity failure")
			}
		})
	}
}

func TestHistoryMixedPagesDrainBoundedSuffixAcrossRecreation(t *testing.T) {
	historyRequests := map[string]int{}
	messageRequests := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			fmt.Fprint(w, `{"emailAddress":"fixture@example.test","historyId":"30"}`)
		case strings.HasSuffix(r.URL.Path, "/labels"):
			fmt.Fprint(w, `{"labels":[{"id":"INBOX","name":"INBOX","type":"system"}]}`)
		case strings.HasSuffix(r.URL.Path, "/history"):
			page := r.URL.Query().Get("pageToken")
			historyRequests[page]++
			refs := []any{}
			start, end := 0, 26
			if page == "p2" {
				start, end = 26, 51
			}
			for i := start; i < end; i++ {
				refs = append(refs, map[string]any{"message": map[string]string{"id": fmt.Sprintf("dead%02d", i)}})
			}
			if page == "" {
				refs = append(refs, map[string]any{"message": map[string]string{"id": "dead00"}})
			}
			rec := map[string]any{"id": "11", "messagesDeleted": refs}
			payload := map[string]any{"history": []any{rec}, "historyId": "30"}
			if page == "" {
				rec["messagesAdded"] = []any{map[string]any{"message": map[string]string{"id": "m1"}}, map[string]any{"message": map[string]string{"id": "m2"}}, map[string]any{"message": map[string]string{"id": "dead00"}}}
				payload["nextPageToken"] = "p2"
			}
			json.NewEncoder(w).Encode(payload)
		case strings.Contains(r.URL.Path, "/messages/"):
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if r.URL.Query().Get("format") == "raw" {
				messageRequests[id]++
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id, "internalDate": "100", "sizeEstimate": 100, "labelIds": []string{"INBOX"}, "raw": rawRFC822(id, "SUBJECT_SENTINEL", "MIME_SENTINEL")})
		default:
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	origin, _ := url.Parse(srv.URL)
	fp, _ := accountFingerprint(gmailProfile{EmailAddress: "fixture@example.test"})
	current := encodeReadyCursor(fp, "10")
	seen := map[string]int{}
	messages := 0
	done := false
	for batch := 0; batch < 20; batch++ {
		b, err := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: localGmailTransport{origin}}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: current, Limit: 7})
		if closeErr := b.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil || len(result.Messages)+len(result.Diagnostics) > 7 {
			t.Fatalf("bounded batch: %#v %v", result, err)
		}
		if !result.Done && bytes.Equal(current, result.Cursor) {
			t.Fatal("repeated cursor prefix")
		}
		for _, d := range result.Diagnostics {
			seen[d.Ref]++
		}
		messages += len(result.Messages)
		current = result.Cursor
		if result.Done {
			done = true
			break
		}
	}
	if !done || len(seen) != 51 || messages != 2 || historyRequests[""] != 1 || historyRequests["p2"] != 1 || !reflect.DeepEqual(messageRequests, map[string]int{"m1": 1, "m2": 1}) {
		t.Fatalf("drain: done=%t deleted=%v messages=%d history=%v get=%v", done, seen, messages, historyRequests, messageRequests)
	}
	for _, n := range seen {
		if n != 1 {
			t.Fatal("duplicated deletion")
		}
	}
	state, err := decodeCursor(current)
	if err != nil || state.Mode != modeReady || state.HistoryID != "30" {
		t.Fatalf("final cursor %#v %v", state, err)
	}
}

func TestHistoryDeletionAndTooLargeMessage(t *testing.T) {
	fake := newFakeClient()
	fake.profile.HistoryID = "20"
	fake.historyPages[historyKey("10", "")] = gmailHistoryPage{History: []gmailHistory{{ID: "11", MessagesDeleted: []gmailMessageRef{{ID: "gone"}}, MessagesAdded: []gmailMessageRef{{ID: "oversized"}}}}}
	fake.messages["oversized"] = gmailMessage{ID: "oversized", SizeEstimate: 1000, Raw: rawRFC822("oversized", "subject", "body")}
	fp, _ := accountFingerprint(fake.profile)
	b := openFakeBackend(t, fake)
	res, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, Cursor: encodeReadyCursor(fp, "10"), MaxMessageBytes: 100, MaxBatchBytes: 100})
	if err != nil || !res.Done || len(res.Messages) != 1 || res.Messages[0].OmittedSize != 1000 {
		t.Fatalf("result: %#v %v", res, err)
	}
	reasons := map[mdbackend.DiagnosticReason]string{}
	for _, d := range res.Diagnostics {
		reasons[d.Reason] = d.Ref
	}
	if reasons[mdbackend.ReasonRemoteRemoved] != "gone" || reasons[mdbackend.ReasonTooLarge] != "oversized" {
		t.Fatalf("diagnostics: %#v", res.Diagnostics)
	}
}
