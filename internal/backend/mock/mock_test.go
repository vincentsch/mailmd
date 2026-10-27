package mock

import (
	"context"
	"errors"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/mail"
)

func TestBatchLimitsStubsAndLifecycle(t *testing.T) {
	m := mail.Message{MessageID: "fixture@invalid", BodyText: "body"}
	d := backend.Diagnostic{Ref: "2", Reason: backend.ReasonParseFailure}
	s := &Service{FolderRecords: []backend.Folder{{Path: "Inbox", ScopeKey: "scope", Fetchable: true}}, Items: map[string][]Item{"scope": {{Message: &m, RawBytes: 100}, {Diagnostic: &d, RawBytes: 100}, {Message: &m, RawBytes: 100}, {Message: &m, RawBytes: 500}}}}
	b, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := backend.FetchRequest{ScopeKey: "scope", FolderPath: "Current", MaxMessageBytes: 200, MaxBatchBytes: 200}
	first, err := b.Fetch(context.Background(), req)
	if err != nil || first.Done || len(first.Messages) != 1 || len(first.Diagnostics) != 1 || first.RawBytes != 200 || first.Messages[0].Folders[0] != "Current" || first.Remaining != 2 {
		t.Fatalf("first batch %#v %v", first, err)
	}
	req.Cursor = first.Cursor
	last, err := b.Fetch(context.Background(), req)
	if err != nil || !last.Done || len(last.Messages) != 2 || last.Messages[1].OmittedSize != 500 || last.Messages[1].BodyText != "" {
		t.Fatalf("last batch %#v %v", last, err)
	}
	if len(last.Diagnostics) != 1 || last.Diagnostics[0].Reason != backend.ReasonTooLarge {
		t.Fatalf("diagnostics %#v", last.Diagnostics)
	}
	req.Cursor = []byte("malformed")
	if _, err = b.Fetch(context.Background(), req); !errors.Is(err, backend.ErrInvalidCursor) {
		t.Fatal("bad cursor accepted")
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Folders(context.Background()); !errors.Is(err, backend.ErrClosed) {
		t.Fatal("closed instance reused")
	}
}

func TestDefaultCountAndBrokenInstanceRequiresFreshOpen(t *testing.T) {
	s := &Service{Items: map[string][]Item{"scope": make([]Item, 26)}}
	for i := range s.Items["scope"] {
		s.Items["scope"][i].RawBytes = 1
	}
	b, _ := s.Open(context.Background())
	first, e := b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: "scope"})
	if e != nil || first.Done || first.RawBytes != 25 {
		t.Fatalf("default count %#v %v", first, e)
	}
	s.FetchErrors = []error{backend.Retryable(errors.New("broken"), 0)}
	if _, e = b.Fetch(context.Background(), backend.FetchRequest{ScopeKey: "scope", Cursor: first.Cursor}); !errors.Is(e, backend.ErrRetryable) {
		t.Fatal(e)
	}
	if _, e = b.Folders(context.Background()); !errors.Is(e, backend.ErrClosed) {
		t.Fatal("failed instance reused")
	}
	_ = b.Close()
	fresh, e := s.Open(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	last, e := fresh.Fetch(context.Background(), backend.FetchRequest{ScopeKey: "scope", Cursor: first.Cursor})
	if e != nil || !last.Done || last.RawBytes != 1 {
		t.Fatalf("fresh %#v %v", last, e)
	}
}
