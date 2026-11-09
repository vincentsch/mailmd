package sync

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/writer"
)

// loadIndex reads index.jsonl. It builds the index again from the Markdown
// files when a line is damaged, or when the index is missing from an archive
// that already has mail. So new lines never go after a broken one.
func (r *run) loadIndex() error {
	entries, damaged, err := readIndex(r.dir)
	switch {
	case err == nil && !damaged:
		r.index = entries
		return nil
	case err != nil && !os.IsNotExist(err):
		return err
	case err != nil && r.saved == nil:
		r.index = map[string]writer.IndexEntry{}
		return nil
	}
	if r.index, err = rebuildIndex(r.dir); err != nil {
		return err
	}
	if r.opts.DryRun {
		return nil
	}
	return compactIndex(r.dir, r.index)
}

// addToIndex appends the entries that are new or changed.
func (r *run) addToIndex(entries []writer.IndexEntry, batch *writer.Batch) error {
	var changed []writer.IndexEntry
	for _, e := range entries {
		if old, ok := r.index[e.Path]; ok && sameEntry(old, e) {
			continue
		}
		r.index[e.Path] = e
		changed = append(changed, e)
	}
	if len(changed) == 0 || r.opts.DryRun {
		return nil
	}
	r.indexed = true
	return appendIndex(r.dir, changed, batch)
}

func sameEntry(a, b writer.IndexEntry) bool {
	return a.Date == b.Date && a.From == b.From && slices.Equal(a.To, b.To) && a.Subject == b.Subject && a.Folder == b.Folder &&
		slices.Equal(a.Labels, b.Labels) && slices.Equal(a.Flags, b.Flags) && a.Thread == b.Thread &&
		a.MessageID == b.MessageID && slices.Equal(a.Attachments, b.Attachments) && a.Omitted == b.Omitted && a.Ref == b.Ref && a.Gone == b.Gone
}

// finishIndex sorts the index and drops repeated lines after new entries
// were added. The index in memory has every line of the file.
func (r *run) finishIndex() error {
	if r.opts.DryRun || !r.indexed {
		return nil
	}
	return compactIndex(r.dir, r.index)
}

// refresh updates the flags of files whose message changed on the server,
// and marks messages that were deleted there. The files of deleted messages
// stay. Backends without Refresher report changes through Fetch instead.
func (r *run) refresh(ctx context.Context, j job, fs *FolderState, fr *FolderResult) error {
	refresher, ok := r.conn.(backend.Refresher)
	if !ok || fs.Cursor == "" {
		return nil
	}
	cursor, err := base64.StdEncoding.DecodeString(fs.Cursor)
	if err != nil {
		return ErrInvalidState
	}
	res, err := refresher.Refresh(ctx, backend.RefreshRequest{ScopeKey: j.key, Cursor: cursor})
	switch {
	case err != nil && (folderOnly(err) || errors.Is(err, backend.ErrScopeNotFound)):
		// The new mail is saved. The flags check waits for the next sync.
		r.closeConnection()
		return nil
	case err != nil && (errors.Is(err, backend.ErrRetryable) || errors.Is(err, backend.ErrClosed)):
		r.closeConnection()
		return nil
	case err != nil:
		return err
	}
	if res.Cursor != nil {
		fs.Cursor = base64.StdEncoding.EncodeToString(res.Cursor)
	}
	if res.Unchanged {
		return r.saveState()
	}
	batch := writer.NewBatch()
	var changed []writer.IndexEntry
	for _, path := range r.sortedPaths() {
		e := r.index[path]
		if e.Folder != j.path || e.Ref == "" {
			continue
		}
		flags, present := res.Flags[e.Ref]
		switch {
		case !present && !e.Gone:
			if err := r.markGone(ctx, &e, true, batch); err != nil {
				return err
			}
			fr.Deleted++
			r.result.Deleted++
		case present && e.Gone:
			if err := r.markGone(ctx, &e, false, batch); err != nil {
				return err
			}
		case present:
			names := writer.FlagNames(flags)
			if slices.Equal(names, e.Flags) {
				continue
			}
			if !r.opts.DryRun {
				if err := keptByYou(writer.UpdateFlags(ctx, filepath.Join(r.dir, filepath.FromSlash(path)), names, batch)); err != nil {
					return err
				}
			}
			e.Flags = names
			fr.Updated++
			r.result.Updated++
		default:
			continue
		}
		changed = append(changed, e)
	}
	if err := r.addToIndex(changed, batch); err != nil {
		return err
	}
	if err := batch.Sync(); err != nil {
		return err
	}
	return r.saveState()
}

// markGone records in the file and its index entry that the server deleted
// the message, or that it is back.
func (r *run) markGone(ctx context.Context, e *writer.IndexEntry, gone bool, batch *writer.Batch) error {
	e.Gone = gone
	if r.opts.DryRun {
		return nil
	}
	day := ""
	if gone {
		day = r.opts.Clock().UTC().Format(time.DateOnly)
	}
	return keptByYou(writer.MarkDeleted(ctx, filepath.Join(r.dir, filepath.FromSlash(e.Path)), day, batch))
}

// keptByYou ignores the error of a change to a file that you deleted or
// replaced with your own text. The index still records the change, and the
// sync goes on.
func keptByYou(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, writer.ErrPathCollision) {
		return nil
	}
	return err
}

// sortedPaths lists the index in a fixed order, so files change in the same
// order on every run.
func (r *run) sortedPaths() []string {
	paths := make([]string, 0, len(r.index))
	for path := range r.index {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// removed marks messages that a provider reported as deleted. It returns the
// other diagnostics, which the sync records as problems. A deletion is
// matched in the folder it was reported for. In Outlook, a message's ID
// stays the same when you move it, so a deletion from Inbox must not mark
// the copy in Archive.
func (r *run) removed(ctx context.Context, j job, fr *FolderResult, diagnostics []backend.Diagnostic) ([]backend.Diagnostic, error) {
	var problems []backend.Diagnostic
	gone := map[string]bool{}
	for _, d := range diagnostics {
		if d.Reason == backend.ReasonRemoteRemoved {
			gone[d.Ref] = true
		} else {
			problems = append(problems, d)
		}
	}
	if len(gone) == 0 {
		return problems, nil
	}
	batch := writer.NewBatch()
	var changed []writer.IndexEntry
	for _, path := range r.sortedPaths() {
		e := r.index[path]
		if e.Ref == "" || !gone[e.Ref] || e.Gone || (!j.shared && e.Folder != j.path) {
			continue
		}
		if err := r.markGone(ctx, &e, true, batch); err != nil {
			return nil, err
		}
		fr.Deleted++
		r.result.Deleted++
		changed = append(changed, e)
	}
	if err := r.addToIndex(changed, batch); err != nil {
		return nil, err
	}
	return problems, batch.Sync()
}
