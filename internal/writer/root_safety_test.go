package writer

import (
	"context"
	"errors"
	"github.com/vincentsch/mailmd/internal/mail"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRootAndAncestorSymlinksRejectedBeforeMutation(t *testing.T) {
	for _, nested := range []bool{false, true} {
		root, outside := t.TempDir(), t.TempDir()
		link := filepath.Join(root, "link")
		if e := os.Symlink(outside, link); e != nil {
			t.Fatal(e)
		}
		target := link
		if nested {
			target = filepath.Join(link, "archive")
		}
		opts := Options{Root: target, Account: "account"}
		for _, operation := range []func(context.Context, Options, mail.Message) (WriteResult, error){PlanMessage, WriteMessage, NewPlanningSession().PlanMessage} {
			if _, e := operation(context.Background(), opts, privateTestMessage()); !errors.Is(e, ErrUnsafePath) {
				t.Fatalf("root link accepted %v", e)
			}
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatal("root link wrote outside archive")
		}
	}
}
