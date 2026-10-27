package writer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/mailfixtures"
	"github.com/vincentsch/mailmd/internal/writer"
)

// fixtureWrite pairs a fixture message with the result of writing it. The
// checks need both, the message for what should happen and the result for
// the paths the writer used.
type fixtureWrite struct {
	message mail.Message
	result  writer.WriteResult
}

// TestFixtureWriteTwiceHarness writes all shared fixtures and checks the
// tree it made. Then it writes them again and checks that no file content or
// time changed.
func TestFixtureWriteTwiceHarness(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seeds := mailfixtures.Messages()
	writes := make([]fixtureWrite, 0, len(seeds))

	for _, seed := range seeds {
		seed := seed
		t.Run("first write "+fixtureName(seed), func(t *testing.T) {
			planned, err := writer.PlanMessage(context.Background(), opts, seed)
			if err != nil {
				t.Fatalf("PlanMessage: %v", err)
			}
			if !planned.Changed {
				t.Fatal("PlanMessage Changed = false, want true")
			}
			for _, file := range planned.Files {
				if file.Status != writer.WriteCreated {
					t.Fatalf("planned file status = %s, want created", file.Status)
				}
				if _, err := os.Stat(file.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("PlanMessage target exists before write %s: %v", file.Path, err)
				}
				for _, att := range file.Attachments {
					if att.Status != writer.WriteCreated && att.Status != writer.WriteSkipped {
						t.Fatalf("planned attachment status = %s, want created or skipped", att.Status)
					}
					if att.Path != "" {
						if _, err := os.Stat(att.Path); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("PlanMessage sidecar exists before write %s: %v", att.Path, err)
						}
					}
				}
			}

			written, err := writer.WriteMessage(context.Background(), opts, seed)
			if err != nil {
				t.Fatalf("WriteMessage: %v", err)
			}
			if !reflect.DeepEqual(planned, written) {
				t.Fatalf("write result differs from plan\nplan: %#v\nwrite:%#v", planned, written)
			}
			for _, file := range written.Files {
				statRegular(t, file.Path)
				for _, att := range file.Attachments {
					if att.Path != "" {
						statRegular(t, att.Path)
					}
				}
			}
			assertResultContained(t, root, written)
			writes = append(writes, fixtureWrite{message: seed, result: written})
		})
	}

	assertExactMarkdownPathSet(t, root, writes)
	assertExactSidecarPathSet(t, root, writes)
	assertFixtureGolden(t, opts, writes, "Quarterly Plan", "fixture-plain.golden.md")
	assertFixtureGolden(t, opts, writes, "Release Notes", "fixture-html.golden.md")
	assertFixtureGolden(t, opts, writes, `> "Quarter": end
Line`, "fixture-hostile.golden.md")
	assertFoldersFrontmatterAndDisk(t, writes)
	assertUnsafeNameFrontmatterAndDisk(t, writes)
	assertNoVersionStamp(t, root)

	mtimes := fileModTimes(t, root)
	for _, seed := range seeds {
		planned, err := writer.PlanMessage(context.Background(), opts, seed)
		if err != nil {
			t.Fatalf("second PlanMessage %q: %v", fixtureName(seed), err)
		}
		assertQuietResult(t, planned)
		written, err := writer.WriteMessage(context.Background(), opts, seed)
		if err != nil {
			t.Fatalf("second WriteMessage %q: %v", fixtureName(seed), err)
		}
		assertQuietResult(t, written)
	}
	if got, want := fileModTimes(t, root), mtimes; !reflect.DeepEqual(got, want) {
		t.Fatalf("unchanged rerun touched files\ngot: %#v\nwant:%#v", got, want)
	}
}

// A change that doesn't touch the local_id updates only the files of that
// message. Other messages' files must stay as they are.
func TestFixtureIsolationForNonIdentityChange(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	writes := writeAllFixtures(t, opts)
	before := fileBytes(t, root)

	changed := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
	changed.Flags.Answered = true
	result, err := writer.WriteMessage(context.Background(), opts, changed)
	if err != nil {
		t.Fatalf("WriteMessage changed seed: %v", err)
	}
	changedPaths := map[string]struct{}{}
	for _, file := range result.Files {
		changedPaths[file.Path] = struct{}{}
	}
	for _, write := range writes {
		if write.message.Subject == changed.Subject {
			continue
		}
		for _, file := range write.result.Files {
			if got := readFile(t, file.Path); !bytes.Equal(got, before[file.Path]) {
				t.Fatalf("other seed markdown changed: %s", file.Path)
			}
			for _, att := range file.Attachments {
				if att.Path != "" {
					if got := readFile(t, att.Path); !bytes.Equal(got, before[att.Path]) {
						t.Fatalf("other seed sidecar changed: %s", att.Path)
					}
				}
			}
		}
	}
	for path := range changedPaths {
		if bytes.Equal(readFile(t, path), before[path]) {
			t.Fatalf("changed seed markdown did not update: %s", path)
		}
	}
}

// Flags and ThreadID are in the frontmatter but not in the local_id. A change
// to them updates the file at the same path, and no new file appears.
func TestFixtureNonIdentityUpdatesStayInPlace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*mail.Message)
	}{
		{name: "flags", mutate: func(m *mail.Message) { m.Flags.Answered = true }},
		{name: "thread id", mutate: func(m *mail.Message) { m.ThreadID = "thread-updated" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			opts := fixtureOptions(root)
			seed := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
			first, err := writer.WriteMessage(context.Background(), opts, seed)
			if err != nil {
				t.Fatalf("first WriteMessage: %v", err)
			}
			beforePaths := markdownPaths(first)

			mutated := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
			tt.mutate(&mutated)
			second, err := writer.WriteMessage(context.Background(), opts, mutated)
			if err != nil {
				t.Fatalf("second WriteMessage: %v", err)
			}
			if !second.Changed {
				t.Fatal("Changed = false, want true")
			}
			if got := markdownPaths(second); !reflect.DeepEqual(got, beforePaths) {
				t.Fatalf("paths changed\ngot: %#v\nwant:%#v", got, beforePaths)
			}
			for _, file := range second.Files {
				if file.Status != writer.WriteUpdated {
					t.Fatalf("file status = %s, want updated", file.Status)
				}
			}
			if got := walkMarkdownFiles(t, root); !sameStringSet(got, beforePaths) {
				t.Fatalf("new markdown path created\ngot: %#v\nwant:%#v", got, beforePaths)
			}
		})
	}
}

// mailmd writes the Markdown files. When you edit one by hand and the
// local_id stays, the next write puts the message's text back.
func TestFixtureManualEditIsRestored(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	path := first.Files[0].Path
	want := readFile(t, path)
	if err := os.WriteFile(path, append(append([]byte{}, want...), []byte("\nmanual edit\n")...), 0o644); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	planned, err := writer.PlanMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	if planned.Files[0].Status != writer.WriteUpdated {
		t.Fatalf("planned status = %s, want updated", planned.Files[0].Status)
	}
	written, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if written.Files[0].Status != writer.WriteUpdated {
		t.Fatalf("written status = %s, want updated", written.Files[0].Status)
	}
	if got := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("manual edit not restored\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A change to a field of the local_id gives a new local_id and so a new file
// name. The writer never deletes files, so the old file stays.
func TestFixtureReidentificationCreatesNewPath(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
	firstPlan, err := writer.PlanMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first PlanMessage: %v", err)
	}
	if _, err := writer.WriteMessage(context.Background(), opts, seed); err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}

	mutated := seedBySubject(t, mailfixtures.Messages(), "Quarterly Plan")
	mutated.Subject = "Quarterly Plan Revised"
	secondPlan, err := writer.PlanMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("second PlanMessage: %v", err)
	}
	second, err := writer.WriteMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("second WriteMessage: %v", err)
	}
	if firstPlan.Files[0].Path == secondPlan.Files[0].Path {
		t.Fatal("identity mutation did not change path")
	}
	if second.Files[0].Status != writer.WriteCreated {
		t.Fatalf("second status = %s, want created", second.Files[0].Status)
	}
	statRegular(t, firstPlan.Files[0].Path)
	statRegular(t, secondPlan.Files[0].Path)
}

// The content of an attachment is neither in the local_id nor in the
// frontmatter. When only the content changes, only that attachment file is
// written again.
func TestFixturePayloadOnlyChangeTouchesOnlySidecar(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	markdown := readFile(t, first.Files[0].Path)

	mutated := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	mutated.Attachments[0].Data = []byte("two")
	second, err := writer.WriteMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("second WriteMessage: %v", err)
	}
	if !second.Changed {
		t.Fatal("Changed = false, want true")
	}
	if second.Files[0].Status != writer.WriteUnchanged {
		t.Fatalf("markdown status = %s, want unchanged", second.Files[0].Status)
	}
	if second.Files[0].Attachments[0].Status != writer.WriteUpdated {
		t.Fatalf("first attachment status = %s, want updated", second.Files[0].Attachments[0].Status)
	}
	if second.Files[0].Attachments[2].Status != writer.WriteUnchanged {
		t.Fatalf("sibling attachment status = %s, want unchanged", second.Files[0].Attachments[2].Status)
	}
	if got := readFile(t, second.Files[0].Path); !bytes.Equal(got, markdown) {
		t.Fatal("markdown changed for payload-only update")
	}
	if got := string(readFile(t, second.Files[0].Attachments[0].Path)); got != "two" {
		t.Fatalf("updated sidecar bytes = %q, want two", got)
	}
}

// Dropping the only payload updates the Markdown file, which then lists the
// attachment as not downloaded. The attachment file already saved stays,
// because the writer never deletes files.
func TestFixturePayloadToggleKeepsTheSavedFile(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Inline Logo")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	sidecarPath := first.Files[0].Attachments[0].Path
	sidecarBytes := readFile(t, sidecarPath)

	mutated := seedBySubject(t, mailfixtures.Messages(), "Inline Logo")
	mutated.Attachments[0].Data = nil
	planned, err := writer.PlanMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	if planned.Files[0].Status != writer.WriteUpdated || planned.Files[0].Attachments[0].Status != writer.WriteSkipped {
		t.Fatalf("planned = %#v", planned.Files[0])
	}
	written, err := writer.WriteMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if !reflect.DeepEqual(planned, written) {
		t.Fatalf("write result differs from plan\nplan: %#v\nwrite:%#v", planned, written)
	}
	if got := readFile(t, sidecarPath); !bytes.Equal(got, sidecarBytes) {
		t.Fatal("the saved attachment changed")
	}
	if !bytes.Contains(readFile(t, written.Files[0].Path), []byte("not downloaded)\n")) {
		t.Fatal("updated markdown does not list the attachment as not downloaded")
	}
	final, err := writer.WriteMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("final WriteMessage: %v", err)
	}
	assertQuietResult(t, final)
}

// An attachment without content still has its number. When the first
// attachment loses its content, the number of the next one doesn't change.
func TestFixturePayloadToggleKeepsSiblingNames(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	firstPath := first.Files[0].Attachments[0].Path
	siblingPath := first.Files[0].Attachments[2].Path
	siblingBytes := readFile(t, siblingPath)

	mutated := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	mutated.Attachments[0].Data = nil
	written, err := writer.WriteMessage(context.Background(), opts, mutated)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	statRegular(t, firstPath)
	if got := readFile(t, siblingPath); !bytes.Equal(got, siblingBytes) {
		t.Fatal("sibling bytes changed")
	}
	if written.Files[0].Attachments[2].Status != writer.WriteUnchanged || filepath.Base(siblingPath) != "03-notes.txt" {
		t.Fatalf("sibling = %#v", written.Files[0].Attachments[2])
	}
}

// Files that someone put beside a message, in its attachments folder or as
// an empty attachments folder, are left alone and do not count as changes.
func TestFixtureForeignFilesBesideAMessageAreKept(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	dir := filepath.Dir(first.Files[0].Attachments[0].Path)
	foreign := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(foreign, []byte("my notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), link); err != nil {
		link = ""
	}

	planned, err := writer.PlanMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	assertQuietResult(t, planned)
	written, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	assertQuietResult(t, written)
	if got := string(readFile(t, foreign)); got != "my notes" {
		t.Fatalf("foreign file = %q", got)
	}
	statDir(t, nested)
	if link != "" {
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("symlink beside the attachments is gone: %v", err)
		}
	}

	empty := seedBySubject(t, mailfixtures.Messages(), "Inline Logo")
	empty.Attachments[0].Data = nil
	result, err := writer.WriteMessage(context.Background(), opts, empty)
	if err != nil {
		t.Fatal(err)
	}
	emptyDir := strings.TrimSuffix(result.Files[0].Path, ".md") + ".attachments"
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	again, err := writer.WriteMessage(context.Background(), opts, empty)
	if err != nil {
		t.Fatal(err)
	}
	assertQuietResult(t, again)
	statDir(t, emptyDir)
}

// Something that is not a regular file at an attachment's path is not safe
// to replace. The write must fail before it changes anything.
func TestFixtureUnsafeAttachmentPathFailsWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{
			name: "directory",
			plant: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
		},
		{
			name: "symlink",
			plant: func(t *testing.T, path string) {
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
					t.Skip("symbolic links are not available:", err)
				}
			},
		},
		{
			name: "fifo",
			plant: func(t *testing.T, path string) {
				if err := makeFIFO(path); errors.Is(err, errors.ErrUnsupported) {
					t.Skip("no named pipes on this system")
				} else if err != nil {
					t.Fatalf("mkfifo: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			opts := fixtureOptions(root)
			seed := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
			first, err := writer.WriteMessage(context.Background(), opts, seed)
			if err != nil {
				t.Fatalf("first WriteMessage: %v", err)
			}
			markdownBytes := readFile(t, first.Files[0].Path)
			target := first.Files[0].Attachments[0].Path
			if err := os.Rename(target, target+".moved"); err != nil {
				t.Fatal(err)
			}
			tt.plant(t, target)

			seed.Flags.Seen = !seed.Flags.Seen
			if _, err = writer.PlanMessage(context.Background(), opts, seed); !errors.Is(err, writer.ErrUnsafePath) {
				t.Fatalf("PlanMessage error = %v, want ErrUnsafePath", err)
			}
			if _, err = writer.WriteMessage(context.Background(), opts, seed); !errors.Is(err, writer.ErrUnsafePath) {
				t.Fatalf("WriteMessage error = %v, want ErrUnsafePath", err)
			}
			if got := readFile(t, first.Files[0].Path); !bytes.Equal(got, markdownBytes) {
				t.Fatal("markdown mutated after unsafe attachment error")
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("planted unsafe entry missing: %v", err)
			}
		})
	}
}

// An unsafe entry in one copy must stop the write before any copy changes.
func TestFixtureAllCopiesPreflightComesFirst(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Project Kickoff")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	before := fileBytes(t, root)
	target := first.Files[1].Attachments[0].Path
	if err := os.Rename(target, target+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	seed.Flags.Seen = !seed.Flags.Seen
	_, err = writer.WriteMessage(context.Background(), opts, seed)
	if !errors.Is(err, writer.ErrUnsafePath) {
		t.Fatalf("WriteMessage error = %v, want ErrUnsafePath", err)
	}
	for path, want := range before {
		if path == target {
			continue
		}
		if got := readFile(t, path); !bytes.Equal(got, want) {
			t.Fatalf("existing file mutated despite preflight error: %s", path)
		}
	}
}

// The folders of a message decide its copies and its frontmatter, but they
// are not part of the local_id. The copy in a folder that the message left
// stays where it is.
func TestFixtureFolderSetChangesUpdateSurvivorsAndLeaveOrphans(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Project Kickoff")
	first, err := writer.WriteMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("first WriteMessage: %v", err)
	}
	archivePath := pathContaining(t, first, "/Archive/")

	added := seedBySubject(t, mailfixtures.Messages(), "Project Kickoff")
	added.Folders = []string{"Work/Projects", "Archive", "News"}
	second, err := writer.WriteMessage(context.Background(), opts, added)
	if err != nil {
		t.Fatalf("add folder WriteMessage: %v", err)
	}
	if len(second.Files) != 3 {
		t.Fatalf("added result files = %d, want 3", len(second.Files))
	}
	statusByPath := map[string]writer.WriteStatus{}
	for _, file := range second.Files {
		statusByPath[file.Path] = file.Status
	}
	if statusByPath[pathContaining(t, second, "/News/")] != writer.WriteCreated {
		t.Fatalf("News copy status = %s, want created", statusByPath[pathContaining(t, second, "/News/")])
	}
	if statusByPath[pathContaining(t, second, "/Archive/")] != writer.WriteUpdated ||
		statusByPath[pathContaining(t, second, "/Work/Projects/")] != writer.WriteUpdated {
		t.Fatalf("survivor statuses after add = %#v, want updated", statusByPath)
	}

	removed := seedBySubject(t, mailfixtures.Messages(), "Project Kickoff")
	removed.Folders = []string{"Work/Projects", "News"}
	third, err := writer.WriteMessage(context.Background(), opts, removed)
	if err != nil {
		t.Fatalf("remove folder WriteMessage: %v", err)
	}
	statRegular(t, archivePath)
	for _, file := range third.Files {
		if file.Status != writer.WriteUpdated {
			t.Fatalf("survivor status = %s, want updated", file.Status)
		}
		if !bytes.Contains(readFile(t, file.Path), []byte(`"News"`)) {
			t.Fatalf("survivor frontmatter missing News folder: %s", file.Path)
		}
	}
	if !bytes.Contains(readFile(t, archivePath), []byte("\"Archive\"")) {
		t.Fatal("removed-folder orphan was rewritten")
	}
}

// A foreign Markdown file at the target path must stop the whole plan before
// attachments are written.
func TestFixtureCollisionPreflightPreventsMutation(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	seed := seedBySubject(t, mailfixtures.Messages(), "Invoice and Notes")
	planned, err := writer.PlanMessage(context.Background(), opts, seed)
	if err != nil {
		t.Fatalf("PlanMessage: %v", err)
	}
	target := planned.Files[0].Path
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	foreign := []byte("---\nlocal_id: \"foreign\"\n---\nforeign body\n")
	if err := os.WriteFile(target, foreign, 0o644); err != nil {
		t.Fatalf("write collision target: %v", err)
	}
	sidecarDir := filepath.Dir(planned.Files[0].Attachments[0].Path)
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatalf("mkdir sidecar: %v", err)
	}
	stale := filepath.Join(sidecarDir, "99-stale.bin")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale sidecar: %v", err)
	}

	_, err = writer.PlanMessage(context.Background(), opts, seed)
	if !errors.Is(err, writer.ErrPathCollision) {
		t.Fatalf("PlanMessage error = %v, want ErrPathCollision", err)
	}
	var perr *writer.PathError
	if !errors.As(err, &perr) || perr.Op != "collision" {
		t.Fatalf("PlanMessage PathError = %#v, want collision", perr)
	}
	_, err = writer.WriteMessage(context.Background(), opts, seed)
	if !errors.Is(err, writer.ErrPathCollision) {
		t.Fatalf("WriteMessage error = %v, want ErrPathCollision", err)
	}
	if !errors.As(err, &perr) || perr.Op != "collision" {
		t.Fatalf("WriteMessage PathError = %#v, want collision", perr)
	}
	if got := readFile(t, target); !bytes.Equal(got, foreign) {
		t.Fatal("colliding file was overwritten")
	}
	statRegular(t, stale)
	for _, att := range planned.Files[0].Attachments {
		if att.Path != "" {
			assertNotExist(t, att.Path)
		}
	}
}

// The twins have the same date and subject in the same folder. Only the
// ShortID in the file name tells their files apart.
func TestFixtureTwinsShareDirectoryButNotBasename(t *testing.T) {
	root := t.TempDir()
	opts := fixtureOptions(root)
	a := seedWhere(t, mailfixtures.Messages(), func(m mail.Message) bool {
		return m.Subject == "Status Update" && m.From.Email == "alice@example.com"
	})
	b := seedWhere(t, mailfixtures.Messages(), func(m mail.Message) bool {
		return m.Subject == "Status Update" && m.From.Email == "bob@example.com"
	})
	ensuredA, err := mail.EnsureLocalID(a)
	if err != nil {
		t.Fatalf("EnsureLocalID A: %v", err)
	}
	ensuredB, err := mail.EnsureLocalID(b)
	if err != nil {
		t.Fatalf("EnsureLocalID B: %v", err)
	}
	if mail.ShortID(ensuredA.LocalID) == mail.ShortID(ensuredB.LocalID) {
		t.Fatal("twin short ids are equal")
	}

	writtenA, err := writer.WriteMessage(context.Background(), opts, a)
	if err != nil {
		t.Fatalf("WriteMessage A: %v", err)
	}
	writtenB, err := writer.WriteMessage(context.Background(), opts, b)
	if err != nil {
		t.Fatalf("WriteMessage B: %v", err)
	}
	pathA := writtenA.Files[0].Path
	pathB := writtenB.Files[0].Path
	if filepath.Dir(pathA) != filepath.Dir(pathB) {
		t.Fatalf("twin dirs differ: %s vs %s", filepath.Dir(pathA), filepath.Dir(pathB))
	}
	if filepath.Base(pathA) == filepath.Base(pathB) {
		t.Fatalf("twin basenames equal: %s", filepath.Base(pathA))
	}
	statRegular(t, pathA)
	statRegular(t, pathB)
}

func fixtureOptions(root string) writer.Options {
	return writer.Options{Root: root, Account: "Test Account"}
}

// writeAllFixtures fills an archive for tests that don't check what the
// first write reported.
func writeAllFixtures(t *testing.T, opts writer.Options) []fixtureWrite {
	t.Helper()
	seeds := mailfixtures.Messages()
	out := make([]fixtureWrite, 0, len(seeds))
	for _, seed := range seeds {
		result, err := writer.WriteMessage(context.Background(), opts, seed)
		if err != nil {
			t.Fatalf("WriteMessage %q: %v", fixtureName(seed), err)
		}
		out = append(out, fixtureWrite{message: seed, result: result})
	}
	return out
}

func seedBySubject(t *testing.T, msgs []mail.Message, subject string) mail.Message {
	t.Helper()
	return seedWhere(t, msgs, func(m mail.Message) bool { return m.Subject == subject })
}

// seedWhere wants exactly one match. If a fixture stops being unique, the
// test fails here and doesn't check the wrong message later.
func seedWhere(t *testing.T, msgs []mail.Message, pred func(mail.Message) bool) mail.Message {
	t.Helper()
	var matches []mail.Message
	for _, msg := range msgs {
		if pred(msg) {
			matches = append(matches, msg)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("seed matches = %d, want 1", len(matches))
	}
	return matches[0]
}

func fixtureName(m mail.Message) string {
	if m.Subject != "" {
		return m.Subject
	}
	return "empty subject"
}

func assertExactMarkdownPathSet(t *testing.T, root string, writes []fixtureWrite) {
	t.Helper()
	var want []string
	for _, write := range writes {
		for _, file := range write.result.Files {
			want = append(want, file.Path)
		}
	}
	got := walkMarkdownFiles(t, root)
	if !sameStringSet(got, want) {
		t.Fatalf("markdown path set mismatch\ngot: %#v\nwant:%#v", got, want)
	}
}

func assertExactSidecarPathSet(t *testing.T, root string, writes []fixtureWrite) {
	t.Helper()
	var want []string
	for _, write := range writes {
		for _, file := range write.result.Files {
			for _, att := range file.Attachments {
				if att.Path != "" {
					want = append(want, att.Path)
				}
			}
		}
	}
	var got []string
	for _, path := range walkRegularFiles(t, root) {
		if !strings.HasSuffix(path, ".md") {
			got = append(got, path)
		}
	}
	if !sameStringSet(got, want) {
		t.Fatalf("sidecar path set mismatch\ngot: %#v\nwant:%#v", got, want)
	}
}

// Golden files have placeholders for the local_ids, which the test computes
// when it runs. So nobody has to write hash values by hand.
func assertFixtureGolden(t *testing.T, opts writer.Options, writes []fixtureWrite, subject, golden string) {
	t.Helper()
	var found fixtureWrite
	count := 0
	for _, write := range writes {
		if write.message.Subject == subject {
			found = write
			count++
		}
	}
	if count != 1 {
		t.Fatalf("fixture %q matches = %d, want 1", subject, count)
	}
	ensured, err := mail.EnsureLocalID(found.message)
	if err != nil {
		t.Fatalf("EnsureLocalID %q: %v", subject, err)
	}
	want := readFile(t, filepath.Join("testdata", golden))
	want = bytes.ReplaceAll(want, []byte("MAILMD_LOCALID"), []byte(ensured.LocalID))
	want = bytes.ReplaceAll(want, []byte("MAILMD_SHORTID"), []byte(mail.ShortID(ensured.LocalID)))
	got := readFile(t, found.result.Files[0].Path)
	if os.Getenv("MAILMD_UPDATE_GOLDEN") == "1" {
		got = bytes.ReplaceAll(got, []byte(ensured.LocalID), []byte("MAILMD_LOCALID"))
		got = bytes.ReplaceAll(got, []byte(mail.ShortID(ensured.LocalID)), []byte("MAILMD_SHORTID"))
		if err := os.WriteFile(filepath.Join("testdata", golden), got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
	_ = opts
}

// The frontmatter has the provider's folder names, and the disk paths have
// the safe names.
func assertFoldersFrontmatterAndDisk(t *testing.T, writes []fixtureWrite) {
	t.Helper()
	write := writeBySubject(t, writes, "Project Kickoff")
	content := readFile(t, write.result.Files[0].Path)
	for _, want := range [][]byte{[]byte(`"Archive"`), []byte(`"Work/Projects"`)} {
		if !bytes.Contains(content, want) {
			t.Fatalf("multi-folder frontmatter missing %q\n%s", want, content)
		}
	}
	if pathContaining(t, write.result, "/Archive/") == "" || pathContaining(t, write.result, "/Work/Projects/") == "" {
		t.Fatal("multi-folder disk paths missing Archive or Work/Projects")
	}
}

// An unsafe provider folder name stays as it is in the frontmatter. On disk
// it becomes a safe name.
func assertUnsafeNameFrontmatterAndDisk(t *testing.T, writes []fixtureWrite) {
	t.Helper()
	write := writeBySubject(t, writes, "Reserved Names")
	content := readFile(t, write.result.Files[0].Path)
	for _, raw := range [][]byte{[]byte(`".."`), []byte(`".git"`), []byte(`".mailmd"`), []byte(`"CON"`)} {
		if !bytes.Contains(content, raw) {
			t.Fatalf("unsafe-name frontmatter missing %q\n%s", raw, content)
		}
	}
	for _, disk := range []string{"/_git-4d0a4dff/", "/_mailmd-0e643ba9/", "/unnamed-5ec1f7e7/", "/_CON-a3dbc4b6/"} {
		if pathContaining(t, write.result, disk) == "" {
			t.Fatalf("unsafe-name disk paths missing %s", disk)
		}
	}
	for _, file := range write.result.Files {
		assertContained(t, filepath.Clean(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file.Path))))), file.Path)
	}
}

func assertNoVersionStamp(t *testing.T, root string) {
	t.Helper()
	for _, path := range walkMarkdownFiles(t, root) {
		if bytes.Contains(readFile(t, path), []byte("mailmd_version")) {
			t.Fatalf("file contains mailmd_version: %s", path)
		}
	}
}

// Result paths are absolute. Check that each of them is inside the root.
func assertResultContained(t *testing.T, root string, result writer.WriteResult) {
	t.Helper()
	for _, file := range result.Files {
		assertContained(t, root, file.Path)
		for _, att := range file.Attachments {
			if att.Path != "" {
				assertContained(t, root, att.Path)
			}
		}
	}
}

func assertQuietResult(t *testing.T, result writer.WriteResult) {
	t.Helper()
	if result.Changed {
		t.Fatalf("Changed = true, want false: %#v", result)
	}
	for _, file := range result.Files {
		if file.Status != writer.WriteUnchanged {
			t.Fatalf("file status = %s, want unchanged", file.Status)
		}
		for _, att := range file.Attachments {
			if att.Status != writer.WriteUnchanged && att.Status != writer.WriteSkipped {
				t.Fatalf("attachment status = %s, want unchanged or skipped", att.Status)
			}
		}
	}
}

func writeBySubject(t *testing.T, writes []fixtureWrite, subject string) fixtureWrite {
	t.Helper()
	var out fixtureWrite
	count := 0
	for _, write := range writes {
		if write.message.Subject == subject {
			out = write
			count++
		}
	}
	if count != 1 {
		t.Fatalf("writes for subject %q = %d, want 1", subject, count)
	}
	return out
}

func pathContaining(t *testing.T, result writer.WriteResult, needle string) string {
	t.Helper()
	for _, file := range result.Files {
		if strings.Contains(filepath.ToSlash(file.Path), needle) {
			return file.Path
		}
	}
	t.Fatalf("no path containing %q in %#v", needle, result.Files)
	return ""
}

func markdownPaths(result writer.WriteResult) []string {
	out := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		out = append(out, file.Path)
	}
	sort.Strings(out)
	return out
}

func walkMarkdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, path := range walkRegularFiles(t, root) {
		if strings.HasSuffix(path, ".md") {
			out = append(out, path)
		}
	}
	return out
}

func walkRegularFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			out = append(out, filepath.Clean(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return reflect.DeepEqual(aa, bb)
}

func fileModTimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	for _, path := range walkRegularFiles(t, root) {
		out[path] = statRegular(t, path).ModTime()
	}
	return out
}

func fileBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, path := range walkRegularFiles(t, root) {
		out[path] = readFile(t, path)
	}
	return out
}

func assertContained(t *testing.T, root, path string) {
	t.Helper()
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		t.Fatalf("path %s is not contained in root %s", path, root)
	}
}

func statRegular(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s mode = %s, want regular", path, info.Mode())
	}
	return info
}

func statDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat dir %s: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists or stat failed unexpectedly: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
