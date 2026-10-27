package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

func graphTestBackend(t *testing.T, handler http.HandlerFunc) (*Backend, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	b, err := Open(context.Background(), Config{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "FIXTURE-TOKEN"}), HTTPClient: server.Client(), BaseURL: server.URL + "/v1.0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, server
}

func TestGraphFoldersPaginationHiddenPathsAndStableScopes(t *testing.T) {
	var origin string
	var requests atomic.Int64
	b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v1.0/me/mailFolders/junkemail":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "hidden"})
			return
		case "/v1.0/me/mailFolders/inbox", "/v1.0/me/mailFolders/sentitems", "/v1.0/me/mailFolders/drafts", "/v1.0/me/mailFolders/archive", "/v1.0/me/mailFolders/deleteditems":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "ErrorFolderNotFound"}})
			return
		}
		if r.URL.Query().Get("includeHiddenFolders") != "true" {
			t.Error("hidden folders omitted")
		}
		page := map[string]any{"value": []any{}}
		switch {
		case r.URL.Path == "/v1.0/me/mailFolders" && r.URL.Query().Get("page") == "":
			page = map[string]any{"value": []any{map[string]any{"id": "r1", "displayName": "Inbox"}}, "@odata.nextLink": origin + "/v1.0/me/mailFolders?includeHiddenFolders=true&page=2"}
		case r.URL.Path == "/v1.0/me/mailFolders":
			page = map[string]any{"value": []any{map[string]any{"id": "r2", "displayName": "Inbox"}}}
		case r.URL.Path == "/v1.0/me/mailFolders/r1/childFolders":
			page = map[string]any{"value": []any{map[string]any{"id": "hidden", "displayName": "Hidden", "parentFolderId": "r1"}}}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	origin = server.URL
	folders, err := b.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 3 || folders[0].Path != "Inbox" || folders[1].Path != "Inbox" || folders[2].Path != "Inbox/Hidden" || folders[0].ScopeKey == folders[1].ScopeKey {
		t.Fatalf("folder paths/scopes=%#v", folders)
	}
	if folders[2].Role != backend.RoleJunk || folders[0].Role != "" {
		t.Fatalf("roles from well-known folders: %#v", folders)
	}
	if requests.Load() != int64(5+len(wellKnownFolders)) {
		t.Fatalf("pagination/children not fully traversed: %d", requests.Load())
	}
	if scopeKey("abc") != "graph-v1:A2FiYw" {
		t.Fatal("frozen scope vector changed")
	}
	if _, err := scopeID(scopeKey("abc") + "AA"); !errors.Is(err, backend.ErrInvalidCursor) {
		t.Fatal("accepted malformed scope")
	}
}

func graphMIME(id string) string {
	return "Message-ID: <" + id + "@example.test>\r\nDate: Fri, 03 Jul 2026 12:00:00 +0000\r\nFrom: Fixture <sender@example.test>\r\nSubject: SUBJECT-SENTINEL\r\nContent-Type: text/plain\r\n\r\nBODY-SENTINEL\r\n"
}

func TestGraphDeltaPendingSuffixIncrementalAndSafeRemovalCursor(t *testing.T) {
	var origin string
	b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Prefer"), `IdType="ImmutableId"`) {
			t.Error("missing ImmutableId preference")
		}
		if strings.HasSuffix(r.URL.Path, "/$value") {
			if strings.Contains(r.URL.Path, "/e/") {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound","message":"BODY-SENTINEL"}}`))
				return
			}
			_, _ = w.Write([]byte(graphMIME("stable")))
			return
		}
		if !strings.Contains(r.Header.Get("Prefer"), "odata.maxpagesize=2") {
			t.Error("missing delta page preference")
		}
		switch r.URL.Query().Get("page") {
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "e"}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?page=done&token=CURSOR-SECRET"})
		case "done":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "a", "isRead": true, "conversationId": "thread1"}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?page=done&token=CURSOR-SECRET"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "c"}, map[string]any{"id": "b"}, map[string]any{"id": "a", "isDraft": true, "flag": map[string]any{"flagStatus": "flagged"}}, map[string]any{"id": "d", "@removed": map[string]any{"reason": "deleted", "body": "CURSOR-LEAK-SENTINEL"}}}, "@odata.nextLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?page=2&token=CURSOR-SECRET"})
		}
	})
	origin = server.URL
	request := backend.FetchRequest{ScopeKey: scopeKey("inbox"), FolderPath: "Inbox", Limit: 2}
	first, err := b.Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Done || len(first.Messages) != 2 || !first.Messages[0].Flags.Draft || !first.Messages[0].Flags.Flagged {
		t.Fatalf("initial delta=%#v", first)
	}
	if strings.Contains(string(first.Cursor), "SUBJECT-SENTINEL") || strings.Contains(string(first.Cursor), "CURSOR-LEAK-SENTINEL") {
		t.Fatal("cursor stored content")
	}
	request.Cursor = first.Cursor
	second, err := b.Fetch(context.Background(), request)
	if err != nil || second.Done || len(second.Messages) != 1 || len(second.Diagnostics) != 1 || second.Diagnostics[0].Reason != backend.ReasonRemoteRemoved {
		t.Fatalf("pending suffix=%#v err=%v", second, err)
	}
	request.Cursor = second.Cursor
	third, err := b.Fetch(context.Background(), request)
	if err != nil || !third.Done || len(third.Diagnostics) != 1 || third.Diagnostics[0].Reason != backend.ReasonVanished {
		t.Fatalf("next/final page=%#v %v", third, err)
	}
	request.Cursor = third.Cursor
	request.FolderPath = "Renamed"
	incremental, err := b.Fetch(context.Background(), request)
	if err != nil || !incremental.Done || len(incremental.Messages) != 1 || incremental.Messages[0].LocalID != first.Messages[0].LocalID || incremental.Messages[0].Folders[0] != "Renamed" || !incremental.Messages[0].Flags.Seen || incremental.Messages[0].ThreadID != "thread1" {
		t.Fatalf("incremental/move=%#v %v", incremental, err)
	}
}

func TestGraphTooLargeMessageKeepsHeadersAndBatchPrefix(t *testing.T) {
	raw := graphMIME("bounds") + strings.Repeat("x", 1024)
	for _, batchPrefix := range []bool{false, true} {
		var origin string
		b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/$value"):
				_, _ = w.Write([]byte(raw))
			case r.URL.Query().Get("$select") == "internetMessageHeaders":
				_ = json.NewEncoder(w).Encode(map[string]any{"internetMessageHeaders": []map[string]string{
					{"name": "Message-ID", "value": "<big@example.test>"}, {"name": "Subject", "value": "Big"}, {"name": "From", "value": "a@example.test"},
				}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]string{"id": "a"}, map[string]string{"id": "b"}}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?done=1"})
			}
		})
		origin = server.URL
		req := backend.FetchRequest{ScopeKey: scopeKey("inbox"), FolderPath: "Inbox", MaxMessageBytes: int64(len(raw) - 1), MaxBatchBytes: int64(2 * len(raw))}
		if batchPrefix {
			req.MaxMessageBytes = int64(len(raw))
			req.MaxBatchBytes = int64(len(raw))
		}
		res, err := b.Fetch(context.Background(), req)
		if batchPrefix {
			if err != nil || res.Done || len(res.Messages) != 1 {
				t.Fatalf("batch prefix=%#v %v", res, err)
			}
			state, err := b.decodeCursor(req.ScopeKey, res.Cursor)
			if err != nil || len(state.Pending) != 1 || state.Pending[0].ID != "b" {
				t.Fatal("deferred position lost")
			}
			continue
		}
		if err != nil || !res.Done || len(res.Messages) != 2 || res.Messages[0].Subject != "Big" || res.Messages[0].OmittedSize == 0 {
			t.Fatalf("too large=%#v %v", res, err)
		}
		if len(res.Diagnostics) != 2 || res.Diagnostics[0].Reason != backend.ReasonTooLarge {
			t.Fatalf("diagnostics=%#v", res.Diagnostics)
		}
	}
}

func TestGraphHTTPClassificationsAndUnsafeLinks(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			b, _ := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"ErrorAccessDenied","message":"PRIVATE-TOKEN"}}`))
			})
			_, err := b.Folders(context.Background())
			want := backend.ErrRetryable
			if status == 401 {
				want = backend.ErrReconnectRequired
			}
			if status == 403 {
				want = backend.ErrPermissionDenied
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "PRIVATE-TOKEN") {
				t.Fatalf("HTTP class=%v", err)
			}
			if status >= 429 {
				var retry *backend.RetryableError
				if !errors.As(err, &retry) || retry.RetryAfter != 2*time.Second {
					t.Fatal("lost retry delay")
				}
			}
		})
	}
	for _, mode := range []string{"pagination", "redirect", "cycle", "malformed-cursor"} {
		t.Run(mode, func(t *testing.T) {
			var origin string
			var requests atomic.Int64
			b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "redirect" {
					http.Redirect(w, r, origin+"/beta/hostile?token=PRIVATE-TOKEN", http.StatusFound)
					return
				}
				if mode == "pagination" {
					_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": origin + "/beta/hostile?token=PRIVATE-TOKEN"})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/childFolders") {
					_, _ = w.Write([]byte(`{"value":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"value":[{"id":"a","displayName":"A","parentFolderId":"b"},{"id":"b","displayName":"B","parentFolderId":"a"}]}`))
			})
			origin = server.URL
			var err error
			if mode == "malformed-cursor" {
				_, err = b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("a"), Cursor: encodeCursor(cursor{Version: 1, Scope: scopeKey("a"), URL: origin + "/beta/hostile?token=PRIVATE-TOKEN"})})
				if requests.Load() != 0 {
					t.Fatal("malformed local cursor used network")
				}
			} else {
				_, err = b.Folders(context.Background())
			}
			if err == nil || errors.Is(err, backend.ErrRetryable) || strings.Contains(err.Error(), "PRIVATE-TOKEN") {
				t.Fatalf("unsafe graph input=%v", err)
			}
		})
	}
}

type refreshFixture struct{ refreshes atomic.Int64 }

func (f *refreshFixture) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "OLD-TOKEN"}, nil
}
func (f *refreshFixture) RefreshContext(context.Context) (*oauth2.Token, error) {
	f.refreshes.Add(1)
	return &oauth2.Token{AccessToken: "NEW-TOKEN"}, nil
}

func TestGraph401RefreshStaleDeltaAndLifecycle(t *testing.T) {
	var origin string
	var stale atomic.Bool
	stale.Store(true)
	source := &refreshFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer OLD-TOKEN" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("stale") == "1" && stale.Swap(false) {
			w.WriteHeader(410)
			_, _ = w.Write([]byte(`{"error":{"code":"ErrorInvalidSyncStateData"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/a/messages/delta?done=1"})
	}))
	defer server.Close()
	origin = server.URL
	b, err := Open(context.Background(), Config{BaseURL: origin + "/v1.0", HTTPClient: server.Client(), TokenSource: source})
	if err != nil {
		t.Fatal(err)
	}
	request := backend.FetchRequest{ScopeKey: scopeKey("a"), Cursor: encodeCursor(cursor{Version: 1, Scope: scopeKey("a"), URL: origin + "/v1.0/me/mailFolders/a/messages/delta?stale=1"})}
	result, err := b.Fetch(context.Background(), request)
	if err != nil || !result.Done || source.refreshes.Load() != 2 {
		t.Fatalf("refresh/reset=%#v %v refreshes=%d", result, err, source.refreshes.Load())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fetch(context.Background(), request); !errors.Is(err, backend.ErrClosed) {
		t.Fatalf("closed=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Folders(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled=%v", err)
	}
	if got := retryAfter("9223372036854775807", time.Now()); got <= 0 {
		t.Fatal("retry duration overflowed")
	}
}

func TestGraphSinceIsAppliedWithoutAFilter(t *testing.T) {
	// A delta query with $filter returns at most 5,000 messages, so mailmd
	// applies --since without one.
	var filter string
	var downloads []string
	var origin string
	b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/$value"):
			downloads = append(downloads, r.URL.Path)
			_, _ = w.Write([]byte("Message-ID: <new@example.test>\r\nSubject: New\r\n\r\nnew\r\n"))
		default:
			filter = r.URL.Query().Get("$filter")
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{
				map[string]any{"id": "old", "receivedDateTime": "2025-01-30T23:59:59Z"},
				map[string]any{"id": "new", "receivedDateTime": "2025-01-31T00:00:00Z"},
			}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?done=1"})
		}
	})
	origin = server.URL
	since := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	res, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("inbox"), FolderPath: "Inbox", Since: since})
	if err != nil {
		t.Fatal(err)
	}
	if filter != "" || len(downloads) != 1 || !strings.Contains(downloads[0], "/new/") || len(res.Messages) != 1 || !res.Done {
		t.Fatalf("filter %q downloads %v result %+v", filter, downloads, res)
	}
}

func TestGraphMessagesCarryRefAndReceivedDate(t *testing.T) {
	var origin string
	b, server := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/$value"):
			// No Date header, and a broken body.
			_, _ = w.Write([]byte("Message-ID: <x@example.test>\r\nSubject: Broken\r\nContent-Transfer-Encoding: base64\r\n\r\n%%% no\r\n"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{
				map[string]any{"id": "AAMk1", "receivedDateTime": "2026-03-04T05:06:07Z"},
			}, "@odata.deltaLink": origin + "/v1.0/me/mailFolders/inbox/messages/delta?done=1"})
		}
	})
	origin = server.URL
	res, err := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("inbox"), FolderPath: "Inbox"})
	if err != nil || len(res.Messages) != 1 {
		t.Fatalf("fetch = %+v, %v", res, err)
	}
	m := res.Messages[0]
	if m.Ref != "AAMk1" || !m.Received.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) || m.ParseError == "" || len(m.Raw) == 0 {
		t.Fatalf("message = %+v", m)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Reason != backend.ReasonParseFailure {
		t.Fatalf("diagnostics = %+v", res.Diagnostics)
	}
}

func TestGraphStalledRequestTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	b, err := Open(context.Background(), Config{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "FIXTURE-TOKEN"}), HTTPClient: server.Client(), BaseURL: server.URL + "/v1.0", IdleTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	start := time.Now()
	if _, err := b.Folders(context.Background()); !errors.Is(err, backend.ErrRetryable) {
		t.Fatalf("Folders() = %v, want a retryable error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the stalled request was not stopped")
	}
}

func TestGraphFetchRefsGetsALargeMessageAgain(t *testing.T) {
	body := "Message-ID: <big@example.test>\r\nSubject: Big\r\n\r\n" + strings.Repeat("x", 3000) + "\r\n"
	b, _ := graphTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/$value"):
			_, _ = w.Write([]byte(body))
		case strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound"}}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "big", "isRead": true, "receivedDateTime": "2026-03-04T05:06:07Z"})
		}
	})
	res, err := b.FetchRefs(context.Background(), backend.FetchRequest{ScopeKey: scopeKey("inbox"), FolderPath: "Inbox", MaxMessageBytes: 10000}, []string{"big", "gone"})
	if err != nil || len(res.Messages) != 1 || res.Messages[0].OmittedSize != 0 || !strings.Contains(res.Messages[0].BodyText, "xxx") || !res.Messages[0].Flags.Seen {
		t.Fatalf("refetch = %+v, %v", res, err)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Ref != "gone" || res.Diagnostics[0].Reason != backend.ReasonVanished {
		t.Fatalf("diagnostics = %+v", res.Diagnostics)
	}
}
