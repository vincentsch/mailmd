// Package graph reads the user's own mailbox through Microsoft Graph. It lists
// the folders and fetches the changes of each folder with delta queries. The
// caller takes care of the login and writes the files.
package graph

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vincentsch/mailmd/internal/backend"
	"golang.org/x/oauth2"
)

var ErrProtocol = errors.New("graph: invalid provider response")

type Config struct {
	TokenSource oauth2.TokenSource
	HTTPClient  *http.Client
	// BaseURL is the address of Graph v1.0, or of a local fake in tests.
	BaseURL string
	Clock   func() time.Time
	// IdleTimeout is how long a request may wait for the next bytes. Zero
	// means backend.DefaultIdleTimeout.
	IdleTimeout time.Duration
}

type Backend struct {
	base   *url.URL
	client *http.Client
	tokens oauth2.TokenSource
	clock  func() time.Time
	mu     sync.Mutex
	op     sync.Mutex
	closed bool
}

var (
	_ backend.Backend   = (*Backend)(nil)
	_ backend.Addresser = (*Backend)(nil)
)

func Open(ctx context.Context, cfg Config) (*Backend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if cfg.TokenSource == nil {
		return nil, errors.New("graph: token source is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://graph.microsoft.com/v1.0"
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("graph: invalid API endpoint")
	}
	if u.Host != "graph.microsoft.com" || u.Scheme != "https" || u.Path != "/v1.0" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("graph: test endpoint must be loopback")
		}
	}
	if !canonicalGraphPath(u) {
		return nil, errors.New("graph: invalid API path")
	}
	// A request fails when Microsoft sends nothing for two minutes. There
	// is no limit on the whole request, so a large message still downloads
	// on a slow line.
	var client http.Client
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.Transport = backend.IdleTimeout(client.Transport, cfg.IdleTimeout)
	b := &Backend{base: u, client: &client, tokens: cfg.TokenSource, clock: cfg.Clock}
	if b.clock == nil {
		b.clock = time.Now
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !b.validURL(req.URL.String()) {
			return ErrProtocol
		}
		return nil
	}
	return b, nil
}

func (b *Backend) Name() string   { return "graph" }
func (b *Backend) isClosed() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.closed }
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.client.CloseIdleConnections()
	return nil
}

func (b *Backend) validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Fragment == "" && u.Opaque == "" && u.Scheme == b.base.Scheme && u.Host == b.base.Host &&
		strings.HasPrefix(u.Path, b.base.Path+"/") && canonicalGraphPath(u)
}

func (b *Backend) endpoint(relative string) string { return b.base.String() + relative }

type folder struct {
	ID     string `json:"id"`
	Name   string `json:"displayName"`
	Parent string `json:"parentFolderId"`
}
type folderPage struct {
	Value []folder `json:"value"`
	Next  string   `json:"@odata.nextLink"`
}

// Address returns the mailbox's email address, or the sign-in name
// (userPrincipalName) of a work account that has no mail address. Microsoft
// answers only when the login includes the User.Read permission, which new
// app registrations have.
func (b *Backend) Address(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.isClosed() {
		return "", backend.ErrClosed
	}
	b.op.Lock()
	defer b.op.Unlock()
	var me struct {
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
	}
	if err := b.json(ctx, b.endpoint("/me?$select=mail,userPrincipalName"), 64<<10, 0, &me); err != nil {
		return "", err
	}
	if me.Mail != "" {
		return me.Mail, nil
	}
	return me.UPN, nil
}

func (b *Backend) Folders(ctx context.Context) ([]backend.Folder, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if b == nil || b.isClosed() {
		return nil, backend.ErrClosed
	}
	b.op.Lock()
	defer b.op.Unlock()
	if b.isClosed() {
		return nil, backend.ErrClosed
	}
	type collection struct{ link, prefix string }
	queue := []collection{{b.endpoint("/me/mailFolders?includeHiddenFolders=true"), ""}}
	seen := map[string]folder{}
	pages := map[string]bool{}
	var output []backend.Folder
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for current.link != "" {
			if pages[current.link] || !b.validURL(current.link) {
				return nil, ErrProtocol
			}
			pages[current.link] = true
			var page folderPage
			if err := b.json(ctx, current.link, backend.DefaultMaxBatchBytes, 0, &page); err != nil {
				return nil, err
			}
			for _, f := range page.Value {
				if !validID(f.ID) || f.Name == "" || seen[f.ID].ID != "" {
					return nil, ErrProtocol
				}
				seen[f.ID] = f
				display := f.Name
				if current.prefix != "" {
					display = current.prefix + "/" + f.Name
				}
				output = append(output, backend.Folder{Path: display, Fetchable: true, ScopeKey: scopeKey(f.ID)})
				queue = append(queue, collection{b.endpoint("/me/mailFolders/" + url.PathEscape(f.ID) + "/childFolders?includeHiddenFolders=true"), display})
			}
			current.link = page.Next
		}
	}
	// A bad answer could make two folders each other's parent. The walk above
	// still ends, so the parents are checked here for loops.
	for id := range seen {
		ancestors := map[string]bool{}
		for current := id; current != "" && seen[current].ID != ""; current = seen[current].Parent {
			if ancestors[current] {
				return nil, ErrProtocol
			}
			ancestors[current] = true
		}
	}
	if err := b.markRoles(ctx, output); err != nil {
		return nil, err
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Path != output[j].Path {
			return output[i].Path < output[j].Path
		}
		return output[i].ScopeKey < output[j].ScopeKey
	})
	return output, nil
}

// Count asks Graph how many messages each folder holds, one request per
// folder.
func (b *Backend) Count(ctx context.Context, folders []backend.Folder) (map[string]int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.isClosed() {
		return nil, backend.ErrClosed
	}
	b.op.Lock()
	defer b.op.Unlock()
	out := map[string]int{}
	for _, f := range folders {
		id, err := scopeID(f.ScopeKey)
		if err != nil {
			continue
		}
		var info struct {
			Total *int `json:"totalItemCount"`
		}
		if err := b.json(ctx, b.endpoint("/me/mailFolders/"+url.PathEscape(id)+"?$select=totalItemCount"), 1<<20, 0, &info); err != nil {
			return nil, err
		}
		if info.Total != nil && *info.Total >= 0 {
			out[f.Path] = *info.Total
		}
	}
	return out, nil
}

// wellKnownFolders are the Graph names of the folders with a role. Folder
// names depend on the mailbox language, so the role comes from Graph.
var wellKnownFolders = []struct {
	name string
	role backend.Role
}{
	{"inbox", backend.RoleInbox},
	{"sentitems", backend.RoleSent},
	{"drafts", backend.RoleDrafts},
	{"archive", backend.RoleArchive},
	{"junkemail", backend.RoleJunk},
	{"deleteditems", backend.RoleTrash},
}

// markRoles looks up the well-known folders and sets their roles, so a sync
// leaves out junk and deleted mail like it does for IMAP. A mailbox without
// one of them, like one without an archive folder, is fine.
func (b *Backend) markRoles(ctx context.Context, folders []backend.Folder) error {
	for _, known := range wellKnownFolders {
		var f folder
		err := b.json(ctx, b.endpoint("/me/mailFolders/"+known.name+"?$select=id"), 1<<20, 0, &f)
		var provider *providerError
		if errors.As(err, &provider) && provider.Status == 404 {
			continue
		}
		if err != nil {
			return err
		}
		for i := range folders {
			if folders[i].ScopeKey == scopeKey(f.ID) {
				folders[i].Role = known.role
			}
		}
	}
	return nil
}

// canonicalGraphPath checks the segments of an escaped path. A segment can be
// an escaped Graph ID, and those can contain characters like / and +. After
// unescaping, as often as needed, a segment must not hold a backslash or a
// control character. No part of it may be "." or "..". The query is not
// checked.
func canonicalGraphPath(u *url.URL) bool {
	raw := u.EscapedPath()
	if raw == "" {
		return true
	}
	if !strings.HasPrefix(raw, "/") {
		return false
	}
	segments := strings.Split(raw[1:], "/")
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return false
		}
		for depth := 0; ; depth++ {
			if strings.Contains(decoded, "\\") || strings.ContainsFunc(decoded, unicode.IsControl) {
				return false
			}
			for _, part := range strings.Split(decoded, "/") {
				if part == "." || part == ".." {
					return false
				}
			}
			if !strings.Contains(decoded, "%") {
				break
			}
			next, err := url.PathUnescape(decoded)
			if err != nil {
				return false
			}
			if next == decoded {
				break
			}
			if depth >= 16 {
				return false
			}
			decoded = next
		}
	}
	return true
}
