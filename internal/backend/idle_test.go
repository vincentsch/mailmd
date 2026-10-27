package backend

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdleTimeoutStopsAStalledRequest(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("start"))
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: IdleTimeout(nil, 100*time.Millisecond)}

	start := time.Now()
	_, err := client.Get(srv.URL + "/headers")
	if !errors.Is(err, ErrStalled) || !IsTransientReadError(err) {
		t.Fatalf("waiting for headers: %v", err)
	}
	resp, err := client.Get(srv.URL + "/body")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("reading the body: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the idle timeout did not stop the requests")
	}
}

func TestIdleTimeoutAllowsASlowSteadyDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: IdleTimeout(nil, 100*time.Millisecond)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || strings.Count(string(data), "chunk") != 6 {
		t.Fatalf("data %q, err %v", data, err)
	}
}
