package gmail

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

type staticTokenSource struct{}

func (staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "access-token"}, nil
}

func TestOpenNameCloseAndConfig(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Open missing auth error = %v, want ErrInvalidConfig", err)
	}

	fake := newFakeClient()
	b := openFakeBackend(t, fake)
	if b.Name() != "gmail" || b.userID != "me" {
		t.Fatalf("backend name/user = %q/%q", b.Name(), b.userID)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := b.Folders(context.Background()); !errors.Is(err, mdbackend.ErrClosed) {
		t.Fatalf("Folders after Close error = %v, want ErrClosed", err)
	}
	if _, err := b.Fetch(context.Background(), mdbackend.FetchRequest{ScopeKey: accountScopeKey}); !errors.Is(err, mdbackend.ErrClosed) {
		t.Fatalf("Fetch after Close error = %v, want ErrClosed", err)
	}

	tokenBackend, err := Open(context.Background(), Config{TokenSource: staticTokenSource{}, UserID: "person"})
	if err != nil {
		t.Fatalf("Open token source: %v", err)
	}
	if tokenBackend.userID != "person" || tokenBackend.ownedHTTPClient == nil {
		t.Fatalf("token backend user/owned client = %q/%v", tokenBackend.userID, tokenBackend.ownedHTTPClient)
	}
	_ = tokenBackend.Close()

	httpBackend, err := Open(context.Background(), Config{
		TokenSource: staticTokenSource{},
		HTTPClient:  &http.Client{},
	})
	if err != nil {
		t.Fatalf("Open HTTP client: %v", err)
	}
	if httpBackend.ownedHTTPClient != nil {
		t.Fatalf("HTTP client backend owns injected client")
	}
	_ = httpBackend.Close()
}

func TestFoldersMapSystemUserUnknownAndCollidingLabels(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{
		{ID: "INBOX", Name: "Inbox", Type: "system"},
		{ID: "SENT", Name: "Sent", Type: "system"},
		{ID: "SPAM", Name: "Spam", Type: "system"},
		{ID: "TRASH", Name: "Trash", Type: "system"},
		{ID: "IMPORTANT", Name: "Important", Type: "system"},
		{ID: "CATEGORY_PERSONAL", Name: "Personal", Type: "system"},
		{ID: "CATEGORY_SOCIAL", Name: "Social", Type: "system"},
		{ID: "CATEGORY_PROMOTIONS", Name: "Promotions", Type: "system"},
		{ID: "CATEGORY_UPDATES", Name: "Updates", Type: "system"},
		{ID: "CATEGORY_FORUMS", Name: "Forums", Type: "system"},
		{ID: "UNREAD", Name: "Unread", Type: "system"},
		{ID: "STARRED", Name: "Starred", Type: "system"},
		{ID: "DRAFT", Name: "Draft", Type: "system"},
		{ID: "CHAT", Name: "Chat", Type: "system"},
		{ID: "u-work", Name: "Work/Projects", Type: "user"},
		{ID: "u-hidden", Name: "Hidden", Type: "user"},
		{ID: "u-empty", Type: "user"},
		{ID: "SYS_EXTRA", Name: "System Extra", Type: "system"},
		{ID: "SYS_EMPTY", Type: "system"},
		{ID: "sys-inbox", Name: "Inbox", Type: "system"},
		{ID: "u-inbox", Name: "Inbox", Type: "user"},
	}
	b := openFakeBackend(t, fake)

	folders, err := b.Folders(context.Background())
	if err != nil {
		t.Fatalf("Folders: %v", err)
	}
	got := make([]string, len(folders))
	scopeKeys := map[string]struct{}{}
	for i, folder := range folders {
		got[i] = folder.Path
		if !folder.Fetchable {
			t.Fatalf("folder %q Fetchable = false", folder.Path)
		}
		scopeKeys[folder.ScopeKey] = struct{}{}
	}
	wantContains := []string{
		"Inbox",
		"Inbox (label " + labelHash("sys-inbox") + ")",
		"Inbox (label " + labelHash("u-inbox") + ")",
		"Sent", "Spam", "Trash", "Important",
		"Categories/Personal", "Categories/Social", "Categories/Promotions", "Categories/Updates", "Categories/Forums",
		"Work/Projects", "Hidden", "System Extra",
		"Unnamed Label (" + labelHash("u-empty") + ")",
		"Unnamed Label (" + labelHash("SYS_EMPTY") + ")",
	}
	for _, want := range wantContains {
		if !stringSliceContains(got, want) {
			t.Fatalf("folders missing %q in %#v", want, got)
		}
	}
	for _, forbidden := range []string{"Unread", "Starred", "Draft", "Chat"} {
		if stringSliceContains(got, forbidden) {
			t.Fatalf("state/excluded label %q returned in folders %#v", forbidden, got)
		}
	}
	if !reflect.DeepEqual(mapKeys(scopeKeys), []string{accountScopeKey}) {
		t.Fatalf("scope keys = %#v", mapKeys(scopeKeys))
	}
}

func TestPerMessageLabelsMapToFoldersAndFlags(t *testing.T) {
	catalog := buildLabelCatalog([]gmailLabel{
		{ID: "INBOX", Name: "Inbox", Type: "system"},
		{ID: "u-work", Name: "Work", Type: "user"},
		{ID: "u-zed", Name: "Zed", Type: "user"},
	})
	msg, err := normalizeGmailMessage(gmailMessage{
		ID:       "m1",
		ThreadID: "thread-1",
		LabelIDs: []string{"STARRED", "UNREAD", "DRAFT", "u-zed", "u-work", "INBOX", "u-work"},
		Raw:      rawRFC822("m1", "Label flags", "body"),
	}, catalog)
	if err != nil {
		t.Fatalf("normalizeGmailMessage: %v", err)
	}
	if got, want := msg.Folders, []string{"Inbox"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folders = %#v, want %#v", got, want)
	}
	if got, want := msg.Labels, []string{"Drafts", "Inbox", "Work", "Zed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
	if msg.Ref != "m1" {
		t.Fatalf("ref = %q", msg.Ref)
	}
	if msg.ThreadID != "thread-1" {
		t.Fatalf("thread id = %q", msg.ThreadID)
	}
	if msg.Flags.Seen || !msg.Flags.Flagged || !msg.Flags.Draft || msg.Flags.Answered {
		t.Fatalf("flags = %#v", msg.Flags)
	}
}

func TestPrimaryFolderOfAMessage(t *testing.T) {
	catalog := buildLabelCatalog([]gmailLabel{
		{ID: "INBOX", Name: "INBOX", Type: "system"},
		{ID: "SENT", Name: "SENT", Type: "system"},
		{ID: "IMPORTANT", Name: "IMPORTANT", Type: "system"},
		{ID: "CATEGORY_PROMOTIONS", Name: "CATEGORY_PROMOTIONS", Type: "system"},
		{ID: "u-work", Name: "Work", Type: "user"},
	})
	for _, tt := range []struct {
		labels []string
		want   string
	}{
		{[]string{"IMPORTANT", "CATEGORY_PROMOTIONS", "INBOX"}, "Inbox"},
		{[]string{"SENT", "u-work"}, "Sent"},
		{[]string{"u-work", "IMPORTANT"}, "Work"},
		{[]string{"CATEGORY_PROMOTIONS"}, "Categories/Promotions"},
		{[]string{"IMPORTANT"}, "Archive"},
		{nil, "Archive"},
	} {
		if folder, _, _ := catalog.labelsAndFlags(tt.labels); folder != tt.want {
			t.Errorf("labels %v: folder %q, want %q", tt.labels, folder, tt.want)
		}
	}
}

func TestAccountFingerprintGoldenVector(t *testing.T) {
	fp, err := accountFingerprint(gmailProfile{EmailAddress: "User@Example.COM"})
	if err != nil {
		t.Fatalf("accountFingerprint: %v", err)
	}
	if got, want := hex.EncodeToString(fp[:]), "19ec9ab4d9cb1e3d78b64ae3b48f01f5525c9a16287c661441b287bf8aecc01c"; got != want {
		t.Fatalf("fingerprint = %s, want %s", got, want)
	}
	if _, err := accountFingerprint(gmailProfile{}); !errors.Is(err, errInvalidProfile) {
		t.Fatalf("empty profile fingerprint error = %v", err)
	}
}

func TestClassifyAPIErrorRetryReconnectAndSanitization(t *testing.T) {
	retry := classifyAPIError(apiErrWithRetryAfter(http.StatusTooManyRequests, "3"))
	var retryable *mdbackend.RetryableError
	if !errors.As(retry, &retryable) || retryable.RetryAfter != 3*time.Second {
		t.Fatalf("429 classified as %T %v retry=%v", retry, retry, retryable)
	}
	for _, err := range []error{
		classifyAPIError(apiErr(http.StatusForbidden, "rateLimitExceeded")),
		classifyAPIError(apiErr(http.StatusForbidden, "quotaExceeded")),
		classifyAPIError(apiErr(http.StatusInternalServerError)),
	} {
		if !errors.Is(err, mdbackend.ErrRetryable) {
			t.Fatalf("error = %v, want retryable", err)
		}
	}
	for _, err := range []error{
		classifyAPIError(apiErr(http.StatusUnauthorized)),
		classifyAPIError(apiErr(http.StatusForbidden, "insufficientPermissions")),
		classifyAPIError(apiErr(http.StatusForbidden, "insufficientScope")),
	} {
		if !errors.Is(err, mdbackend.ErrReconnectRequired) {
			t.Fatalf("error = %v, want reconnect", err)
		}
	}
	for _, err := range []error{
		classifyAPIError(apiErr(http.StatusBadRequest, "invalidArgument")),
		classifyAPIError(errors.New("Authorization: Bearer secret-token")),
	} {
		text := err.Error()
		for _, forbidden := range []string{"secret-token", "raw MIME", "attachment bytes", "Authorization"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("classified error leaked %q: %v", forbidden, err)
			}
		}
	}
	if got := classifyAPIError(context.Canceled); got != context.Canceled {
		t.Fatalf("context canceled classified as %v", got)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	if len(out) > 1 && out[0] > out[1] {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

func TestCountAsksForEachLabelsTotal(t *testing.T) {
	fake := newFakeClient()
	fake.labels = []gmailLabel{
		{ID: "INBOX", Name: "INBOX", Type: "system"},
		{ID: "SPAM", Name: "SPAM", Type: "system"},
		{ID: "u-work", Name: "Work", Type: "user"},
	}
	fake.totals = map[string]int{"INBOX": 7, "SPAM": 2, "u-work": 0}
	b := openFakeBackend(t, fake)
	folders, err := b.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counts, err := b.Count(context.Background(), folders)
	if err != nil {
		t.Fatal(err)
	}
	// Archive is mailmd's folder for mail without a label, so Gmail can't
	// count it.
	if !reflect.DeepEqual(counts, map[string]int{"Inbox": 7, "Spam": 2, "Work": 0}) {
		t.Fatalf("counts = %v", counts)
	}
}
