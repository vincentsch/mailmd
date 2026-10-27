package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestGmailMemoryOnlyRefreshAndCancellation(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.TokenExpiry = time.Now().Add(-time.Hour)
	if e := storeGmailCredential(context.Background(), paths, cred); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(paths.CredentialPath)
	if e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(paths.CredentialPath)
	endpoint := successfulTokenEndpoint(t, "new-access", "rotated-refresh")
	loaded, e := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: endpoint.Client(), Persistence: MemoryOnlyTokens})
	if e != nil {
		t.Fatal(e)
	}
	source := loaded.TokenSource.(*persistingTokenSource)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = source.TokenContext(ctx); !errors.Is(e, context.Canceled) || endpoint.Count() != 0 {
		t.Fatalf("cancel %v requests %d", e, endpoint.Count())
	}
	for range 2 {
		token, e := source.TokenContext(context.Background())
		if e != nil || token.AccessToken != "new-access" || token.RefreshToken != "rotated-refresh" {
			t.Fatalf("memory refresh %v", e)
		}
	}
	after, _ := os.ReadFile(paths.CredentialPath)
	afterInfo, _ := os.Stat(paths.CredentialPath)
	if string(before) != string(after) || info.Mode() != afterInfo.Mode() || !info.ModTime().Equal(afterInfo.ModTime()) || endpoint.Count() != 1 {
		t.Fatal("memory-only refresh changed files or reissued grant")
	}
}

func TestGmailRotationPersistenceFailureRetainsGrantForRetry(t *testing.T) {
	root := t.TempDir()
	paths := mustCredentialPaths(t, root, "acct")
	cred := validCredential()
	cred.TokenExpiry = time.Now().Add(-time.Hour)
	if e := storeGmailCredential(context.Background(), paths, cred); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(paths.CredentialPath)
	endpoint := successfulTokenEndpoint(t, "new-access", "rotated-refresh")
	loaded, e := LoadGmailToken(context.Background(), GmailLoadOptions{ConfigRoot: root, ArchiveDir: "acct", HTTPClient: endpoint.Client()})
	if e != nil {
		t.Fatal(e)
	}
	original := renameCredentialFile
	defer func() { renameCredentialFile = original }()
	failed := false
	renameCredentialFile = func(from, to string) error {
		if !failed {
			failed = true
			return errors.New("injected")
		}
		return os.Rename(from, to)
	}
	source := loaded.TokenSource.(*persistingTokenSource)
	if token, e := source.TokenContext(context.Background()); e == nil || token != nil {
		t.Fatal("undurable rotation became usable")
	}
	after, _ := os.ReadFile(paths.CredentialPath)
	if string(before) != string(after) {
		t.Fatal("failed rotation changed old credential")
	}
	token, e := source.TokenContext(context.Background())
	if e != nil || token.AccessToken != "new-access" || endpoint.Count() != 1 {
		t.Fatalf("rotation retry %v grants %d", e, endpoint.Count())
	}
	stored := readStoredCredential(t, paths.CredentialPath)
	if stored.RefreshToken != "rotated-refresh" {
		t.Fatal("rotation not durable before use")
	}
}
