package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/oauth2"
)

// gmailLoopbackFlow is one Gmail sign-in. It runs a small local web server
// that receives Google's answer, and it holds the values mailmd needs after
// that.
type gmailLoopbackFlow struct {
	Config   oauth2.Config
	AuthURL  string
	Verifier string

	state    string
	complete func(loopbackResult) bool
	server   *http.Server
	result   <-chan loopbackResult
	close    func()
}

type loopbackResult struct {
	code string
	err  error
}

// startGmailLoopback starts the local web server on a free port of 127.0.0.1
// and builds the sign-in address. The caller shows the address and may open
// a browser.
func startGmailLoopback(ctx context.Context, client desktopOAuthClient) (*gmailLoopbackFlow, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, oauthSetup("mailmd could not start its local sign-in page")
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, oauthSetup("mailmd could not start its local sign-in page")
	}
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d", tcp.Port)
	redirect, err := url.Parse(redirectURL)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	callbackPath := redirect.Path
	if callbackPath == "" {
		callbackPath = "/"
	}
	state, err := randomState()
	if err != nil {
		_ = ln.Close()
		return nil, oauthSetup("mailmd could not prepare the sign-in")
	}
	verifier := oauth2.GenerateVerifier()
	conf := gmailOAuthConfig(client, redirectURL)
	resultCh := make(chan loopbackResult, 1)

	var mu sync.Mutex
	completed := false
	complete := func(res loopbackResult) bool {
		// Only the first answer finishes the sign-in. When the browser sends it
		// again, it gets the "already finished" page.
		mu.Lock()
		defer mu.Unlock()
		if completed {
			return false
		}
		completed = true
		resultCh <- res
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Ignore other hosts and paths, so another local request can't finish
		// this sign-in by mistake.
		if r.Host != redirect.Host || r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("state") != state {
			// An answer with the wrong state gets an error page, but the
			// sign-in goes on. The right answer can still come later.
			writeLoopbackPage(w, http.StatusBadRequest, "Sign-in failed", "This answer doesn't belong to the sign-in that mailmd is waiting for. You can close this tab.")
			return
		}
		if oauthErr := query.Get("error"); oauthErr != "" {
			code := sanitizeOAuthCode(oauthErr)
			if complete(loopbackResult{err: oauthSetup("Google stopped the sign-in (%s)", code)}) {
				writeLoopbackPage(w, http.StatusBadRequest, "Sign-in failed", "Google did not give mailmd access. You can close this tab and go back to the terminal.")
				return
			}
			writeLoopbackPage(w, http.StatusOK, "Sign-in already finished", "You can close this tab and go back to the terminal.")
			return
		}
		code := query.Get("code")
		if code == "" {
			if complete(loopbackResult{err: oauthSetup("Google's answer has no sign-in code")}) {
				writeLoopbackPage(w, http.StatusBadRequest, "Sign-in failed", "Google's answer has no sign-in code. You can close this tab and go back to the terminal.")
				return
			}
			writeLoopbackPage(w, http.StatusOK, "Sign-in already finished", "You can close this tab and go back to the terminal.")
			return
		}
		if complete(loopbackResult{code: code}) {
			writeLoopbackPage(w, http.StatusOK, "Sign-in complete", "You can close this tab and go back to the terminal.")
			return
		}
		writeLoopbackPage(w, http.StatusOK, "Sign-in already finished", "You can close this tab and go back to the terminal.")
	})

	server := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			complete(loopbackResult{err: oauthSetup("mailmd's local sign-in page stopped working")})
		}
	}()
	closeFn := func() {
		_ = server.Close()
		<-done
	}
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	return &gmailLoopbackFlow{
		Config:   conf,
		AuthURL:  authURL,
		Verifier: verifier,
		state:    state,
		complete: complete,
		server:   server,
		result:   resultCh,
		close:    closeFn,
	}, nil
}

// Paste completes the flow with the address the browser ended up on after
// the user allowed access. On a computer without a browser, the user signs
// in on another device, where that page on 127.0.0.1 does not load, and
// copies its address instead.
func (f *gmailLoopbackFlow) Paste(raw string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		f.complete(loopbackResult{err: oauthSetup("the pasted text is not an address")})
		return
	}
	query := u.Query()
	switch {
	case query.Get("state") != f.state:
		f.complete(loopbackResult{err: oauthSetup("the pasted address does not belong to this sign-in")})
	case query.Get("error") != "":
		f.complete(loopbackResult{err: oauthSetup("Google stopped the sign-in (%s)", sanitizeOAuthCode(query.Get("error")))})
	case query.Get("code") == "":
		f.complete(loopbackResult{err: oauthSetup("the pasted address has no sign-in code")})
	default:
		f.complete(loopbackResult{code: query.Get("code")})
	}
}

// Wait returns the sign-in code from Google's answer, or the error that ended
// the sign-in.
func (f *gmailLoopbackFlow) Wait(ctx context.Context) (string, error) {
	select {
	case res := <-f.result:
		if res.err != nil {
			return "", res.err
		}
		return res.code, nil
	case <-ctx.Done():
		f.Close()
		return "", ctx.Err()
	}
}

// Close stops the local web server and waits until it has stopped. A second
// call does nothing.
func (f *gmailLoopbackFlow) Close() {
	if f == nil || f.close == nil {
		return
	}
	f.close()
	f.close = nil
}

func randomState() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func writeLoopbackPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>", html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

// sanitizeOAuthCode makes an error code from the provider safe to show. It
// drops every character other than letters, digits and _-. and stops after
// 80 characters.
func sanitizeOAuthCode(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
			if b.Len() >= 80 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "oauth_error"
	}
	return b.String()
}
