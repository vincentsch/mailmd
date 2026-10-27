package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGraphCancellationWhilePollingLeavesNoCredential(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/devicecode") {
			io.WriteString(w, `{"device_code":"PRIVATE-DEVICE","user_code":"PUBLIC-CODE","verification_uri":"https://microsoft.com/devicelogin","expires_in":600,"interval":1}`)
			return
		}
		polls.Add(1)
		cancel()
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"authorization_pending","error_description":"PRIVATE-DEVICE"}`)
	}))
	defer server.Close()
	root := t.TempDir()
	_, e := ConnectGraph(ctx, GraphConnectOptions{ClientID: graphFixtureClient, ConfigRoot: root, ArchiveDir: "acct", IdentityBaseURL: server.URL, GraphBaseURL: server.URL, HTTPClient: server.Client(), HandleDeviceCode: func(context.Context, DeviceCodePrompt) error { return nil }})
	if !errors.Is(e, context.Canceled) || polls.Load() != 1 {
		t.Fatalf("poll cancellation %v polls %d", e, polls.Load())
	}
	if _, e = os.Stat(filepath.Join(root, "mailmd", "acct", "graph-token.json")); !os.IsNotExist(e) {
		t.Fatal("canceled polling stored credential")
	}
}
