package accounts

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/gmail"
	"github.com/vincentsch/mailmd/internal/backend/graph"
	"github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"golang.org/x/oauth2"
)

type OpenOptions struct {
	ConfigRoot                    string
	Persistence                   auth.TokenPersistence
	HTTPClient                    *http.Client
	TokenSource                   oauth2.TokenSource
	Clock                         func() time.Time
	GraphBaseURL, IdentityBaseURL string
	RootCAs                       *x509.CertPool
	Mock                          func(context.Context) (backend.Backend, error)
	// Log gets the details that --verbose shows.
	Log io.Writer
}
type IMAPCredential struct {
	Schema   string `json:"schema"`
	Password string `json:"password"`
}

func StoreIMAPCredential(root, stage, password string) error {
	if password == "" || !ValidSegment(stage) {
		return ErrRegistry
	}
	dir := filepath.Join(root, "mailmd", stage)
	if e := privatefs.EnsureDir(root, dir, privatefs.Hooks{}); e != nil {
		return e
	}
	data, _ := json.Marshal(IMAPCredential{Schema: "mailmd-imap-token/1", Password: password})
	return privatefs.Replace([]privatefs.Replacement{{Path: filepath.Join(dir, "imap.json"), Data: data}}, privatefs.Hooks{})
}

// OpenBackend connects to the provider of an account. A mock account works
// only with opts.Mock from the caller, so it never ends up at a real provider.
func OpenBackend(ctx context.Context, a AccountConfig, opts OpenOptions) (backend.Backend, error) {
	if validate(Registry{Schema: Schema, Accounts: []AccountConfig{a}}) != nil {
		return nil, ErrRegistry
	}
	switch a.Backend {
	case "mock":
		if opts.Mock == nil {
			return nil, ErrRegistry
		}
		return opts.Mock(ctx)
	case "imap":
		data, e := privatefs.Read(filepath.Join(opts.ConfigRoot, "mailmd", a.CredentialRef), 4<<20)
		if e != nil {
			if os.IsNotExist(e) {
				return nil, auth.ErrCredentialNotFound
			}
			return nil, privatefs.ErrIntegrity
		}
		info, err := os.Lstat(filepath.Join(opts.ConfigRoot, "mailmd", a.CredentialRef))
		if err != nil || !privatefs.HasMode(info.Mode(), 0o600) {
			return nil, auth.ErrInsecurePermission
		}
		var cred IMAPCredential
		if privatefs.Decode(data, &cred) != nil || cred.Schema != "mailmd-imap-token/1" || cred.Password == "" {
			return nil, auth.ErrOAuthSetup
		}
		return imap.Open(ctx, imap.Config{
			Host:     a.Settings.Host,
			Port:     a.Settings.Port,
			Username: a.Settings.Username,
			Password: cred.Password,
			TLSMode:  imap.TLSMode(a.Settings.TLSMode),
			RootCAs:  opts.RootCAs,
			Log:      opts.Log,
		})
	case "gmail":
		ts := opts.TokenSource
		if ts == nil {
			token, e := auth.LoadGmailToken(ctx, auth.GmailLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: a.ArchiveDir, HTTPClient: opts.HTTPClient, Persistence: opts.Persistence})
			if e != nil {
				return nil, e
			}
			ts = token.TokenSource
		}
		var transport http.RoundTripper
		if opts.HTTPClient != nil {
			transport = opts.HTTPClient.Transport
		}
		return gmail.Open(ctx, gmail.Config{TokenSource: ts, Transport: transport})
	case "graph":
		ts := opts.TokenSource
		if ts == nil {
			token, e := auth.LoadGraphToken(ctx, auth.GraphLoadOptions{ConfigRoot: opts.ConfigRoot, ArchiveDir: a.ArchiveDir, HTTPClient: opts.HTTPClient, Persistence: opts.Persistence, IdentityBaseURL: opts.IdentityBaseURL, Clock: opts.Clock})
			if e != nil {
				return nil, e
			}
			ts = token.TokenSource
		}
		return graph.Open(ctx, graph.Config{TokenSource: ts, HTTPClient: opts.HTTPClient, BaseURL: opts.GraphBaseURL, Clock: opts.Clock})
	}
	return nil, ErrRegistry
}
