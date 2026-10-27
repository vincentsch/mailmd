package gmail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

func decodedFixture(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := decodeRawMessage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGmailByteLimitsPrefixAndRaisedRecovery(t *testing.T) {
	small := decodedFixture(t, rawRFC822("small", "BOUND-SUBJECT", "body"))
	large := small + strings.Repeat("x", 100)
	for _, tt := range []struct {
		name, first, second string
		maximum, batch      int64
	}{
		{"exact", small, "", int64(len(small)), int64(len(small))},
		{"excess-first", large, "", int64(len(large) - 1), int64(len(large))},
		{"oversize-later", small, large, int64(len(large) - 1), int64(2 * len(large))},
		{"batch-prefix", small, small, int64(len(small)), int64(len(small))},
		{"malformed-counted", strings.Repeat("!", len(small)), small, int64(len(small)), int64(len(small))},
		{"attachment-excess", decodedFixture(t, attachmentRFC822("a", "BOUND-SUBJECT")), "", 10, 10000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeClient()
			fake.messages["a"] = gmailMessage{ID: "a", Raw: rawBase64(tt.first)}
			ids := []gmailMessageRef{{ID: "a"}}
			if tt.second != "" {
				fake.messages["b"] = gmailMessage{ID: "b", Raw: rawBase64(tt.second)}
				ids = append(ids, gmailMessageRef{ID: "b"})
			}
			fake.messagePages[""] = gmailMessagePage{Messages: ids}
			b := openFakeBackend(t, fake)
			req := mdbackend.FetchRequest{ScopeKey: accountScopeKey, MaxMessageBytes: tt.maximum, MaxBatchBytes: tt.batch}
			result, err := b.Fetch(context.Background(), req)
			if tt.name == "excess-first" || tt.name == "attachment-excess" {
				// A message over the limit is saved with its headers only.
				if err != nil || !result.Done || len(result.Messages) != 1 || result.Messages[0].OmittedSize == 0 ||
					result.Messages[0].BodyText != "" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Reason != mdbackend.ReasonTooLarge {
					t.Fatalf("oversized: %#v %v", result, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if tt.name == "exact" && (len(result.Messages) != 1 || len(result.Diagnostics) != 0 || !result.Done) {
					t.Fatalf("exact fit was not normalized: %#v", result)
				}
				if tt.name == "oversize-later" {
					if !result.Done || len(result.Messages) != 2 || result.Messages[0].OmittedSize+result.Messages[1].OmittedSize == 0 {
						t.Fatalf("a later message over the limit should be saved with its headers: %#v", result)
					}
				} else if tt.second != "" {
					if result.Done || len(result.Messages) != 1 {
						t.Fatalf("prefix: %#v", result)
					}
					// A message that can't be read is still saved, with a diagnostic.
					if tt.name == "malformed-counted" && (result.Messages[0].ParseError == "" || len(result.Messages[0].Raw) == 0 || len(result.Diagnostics) != 1) {
						t.Fatalf("malformed fixture was not kept: %#v", result)
					}
					if tt.name != "malformed-counted" && (len(result.Diagnostics) != 0 || result.Messages[0].ParseError != "") {
						t.Fatalf("accepted prefix was not a normalized message: %#v", result)
					}
					cursor, err := decodeCursor(result.Cursor)
					if err != nil || len(cursor.PendingIDs) != 1 || cursor.PendingIDs[0] != "b" {
						t.Fatalf("pending oversized/deferred position lost: %#v %v", cursor, err)
					}
					req.Cursor = result.Cursor
					if _, err := b.Fetch(context.Background(), req); err != nil {
						t.Fatalf("next batch: %v", err)
					}
				}
			}
			req.MaxMessageBytes = 10000
			req.MaxBatchBytes = 20000
			res, err := b.Fetch(context.Background(), req)
			if err != nil || !res.Done {
				t.Fatalf("raised limit: %#v %v", res, err)
			}
		})
	}
}

type localGmailTransport struct{ origin *url.URL }

func (t localGmailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = t.origin.Scheme
	u.Host = t.origin.Host
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}

func TestGmailActualHTTPBoundsAndSizePreflight(t *testing.T) {
	raw := decodedFixture(t, rawRFC822("http", "HTTP-SUBJECT", "HTTP-BODY"))
	for _, mode := range []string{"exact", "unknown-size-excess", "known-size-excess", "oversized-envelope", "oversized-metadata"} {
		t.Run(mode, func(t *testing.T) {
			var bodyCalls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/profile"):
					if mode == "oversized-metadata" {
						_, _ = io.WriteString(w, strings.Repeat("X", 10000))
						return
					}
					_, _ = io.WriteString(w, `{"emailAddress":"fixture@example.test","historyId":"10"}`)
				case strings.HasSuffix(r.URL.Path, "/labels"):
					_, _ = io.WriteString(w, `{"labels":[]}`)
				case strings.HasSuffix(r.URL.Path, "/messages"):
					_, _ = io.WriteString(w, `{"messages":[{"id":"a"}]}`)
				case r.URL.Query().Get("format") == "metadata" && !strings.Contains(r.URL.Query().Get("fields"), "sizeEstimate"):
					_, _ = io.WriteString(w, `{"id":"a","threadId":"t","labelIds":["INBOX"],"payload":{"headers":[{"name":"Subject","value":"HTTP-SUBJECT"},{"name":"Message-ID","value":"<http@example.test>"},{"name":"From","value":"a@example.test"}]}}`)
				case r.URL.Query().Get("format") == "metadata":
					size := int64(0)
					if mode == "known-size-excess" {
						size = int64(len(raw) + 1)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"sizeEstimate": size})
				case r.URL.Query().Get("format") == "raw":
					bodyCalls.Add(1)
					if mode == "oversized-envelope" {
						_, _ = io.WriteString(w, strings.Repeat("X", 10000))
						return
					}
					data := raw
					if mode == "unknown-size-excess" {
						data += "x"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "a", "raw": rawBase64(data)})
				default:
					http.Error(w, "unexpected route", 500)
				}
			}))
			defer srv.Close()
			origin, _ := url.Parse(srv.URL)
			b, err := Open(context.Background(), Config{HTTPClient: &http.Client{Transport: localGmailTransport{origin}}})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			result, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey, MaxMessageBytes: int64(len(raw)), MaxBatchBytes: int64(2 * len(raw))})
			if mode == "exact" {
				if err != nil || len(result.Messages) != 1 {
					t.Fatalf("exact HTTP fit: %#v %v", result, err)
				}
			} else if mode == "oversized-metadata" {
				if err == nil || len(result.Cursor) != 0 {
					t.Fatalf("unbounded metadata accepted: %#v %v", result, err)
				}
			} else if err != nil || len(result.Messages) != 1 || result.Messages[0].Subject != "HTTP-SUBJECT" || result.Messages[0].OmittedSize == 0 {
				t.Fatalf("HTTP oversize: %#v %v", result, err)
			}
			if mode == "known-size-excess" && bodyCalls.Load() != 0 {
				t.Fatal("ignored size preflight")
			}
			if err != nil && (strings.Contains(err.Error(), "HTTP-SUBJECT") || strings.Contains(err.Error(), "HTTP-BODY") || strings.Contains(err.Error(), srv.URL)) {
				t.Fatal("HTTP error leaked source or URL")
			}
		})
	}
}
