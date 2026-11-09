package writer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/vincentsch/mailmd/internal/mail"
)

func TestPlanningSessionMatchesSequentialApplyWithoutDiskMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	opts := Options{Root: root, Account: "account"}
	m := privateTestMessage()
	m.Attachments = []mail.Attachment{{Filename: "payload.bin", Data: []byte("first")}}
	initial, e := WriteMessage(ctx, opts, m)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Dir(initial.Files[0].Path), 0o755); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(initial.Files[0].Path, 0o644); e != nil {
		t.Fatal(e)
	}
	before := treeSnapshot(t, root)
	session := NewPlanningSession()
	update := m
	update.Attachments = []mail.Attachment{{Filename: "payload.bin", Data: []byte("second")}}
	removed := update
	removed.Attachments = []mail.Attachment{{Filename: "payload.bin"}}
	messages := []mail.Message{m, m, update, update, removed, removed}
	plans := make([]WriteResult, len(messages))
	for i, message := range messages {
		plans[i], e = session.PlanMessage(ctx, opts, message)
		if e != nil {
			t.Fatal(e)
		}
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("planning mutated disk")
	}
	for i, message := range messages {
		actual, e := WriteMessage(ctx, opts, message)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(actual, plans[i]) {
			t.Fatalf("position %d: apply %#v, plan %#v", i, actual, plans[i])
		}
	}
	// Windows has no Unix modes, so the first write has nothing to repair
	// there.
	repair := runtime.GOOS != "windows"
	if plans[0].Changed != repair || plans[1].Changed || !plans[2].Changed || plans[3].Changed || !plans[4].Changed || plans[5].Changed {
		t.Fatal("sequence did not exercise repairs, updates and removals")
	}
}

type snapshotEntry struct {
	Mode  os.FileMode
	Data  string
	Mtime int64
}

func treeSnapshot(t *testing.T, root string) map[string]snapshotEntry {
	t.Helper()
	result := map[string]snapshotEntry{}
	if e := filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		entry := snapshotEntry{Mode: info.Mode(), Mtime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			entry.Data = string(data)
		}
		result[path] = entry
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return result
}

func TestPlanningSessionRetainsCollisionAndSymlinkGuards(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	opts := Options{Root: root, Account: "account"}
	m := privateTestMessage()
	s := NewPlanningSession()
	r, e := s.PlanMessage(ctx, opts, m)
	if e != nil {
		t.Fatal(e)
	}
	// Another local_id with the same short file name is still a collision.
	other := m
	other.LocalID = r.LocalID[:len(r.LocalID)-1] + "a"
	if other.LocalID == r.LocalID {
		other.LocalID = r.LocalID[:len(r.LocalID)-1] + "b"
	}
	if _, e = s.PlanMessage(ctx, opts, other); e == nil {
		t.Fatal("overlay hid identity collision")
	}
	root2 := t.TempDir()
	if e = os.Symlink(root, filepath.Join(root2, "account")); e != nil {
		t.Fatal(e)
	}
	if _, e = NewPlanningSession().PlanMessage(ctx, Options{Root: root2, Account: "account"}, m); e == nil {
		t.Fatal("overlay followed symlink")
	}
}
