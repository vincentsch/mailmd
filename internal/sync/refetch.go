package sync

import (
	"context"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/writer"
)

// refetchStubs fetches the full version of messages that were saved with
// their headers only, once the size limit is high enough for them. Each
// full message replaces the file of its stub, so no second file appears.
func (r *run) refetchStubs(ctx context.Context, j job, fr *FolderResult, request backend.FetchRequest) error {
	var stubs []writer.IndexEntry
	for _, path := range r.sortedPaths() {
		e := r.index[path]
		if e.Omitted > 0 && e.Omitted <= request.MaxMessageBytes && e.Ref != "" && !e.Gone && (j.shared || e.Folder == j.path) {
			stubs = append(stubs, e)
		}
	}
	for len(stubs) > 0 {
		chunk := stubs[:min(len(stubs), request.Limit)]
		stubs = stubs[len(chunk):]
		refs := make([]string, len(chunk))
		r.keepIn = map[string]writer.IndexEntry{}
		for i, e := range chunk {
			refs[i] = e.Ref
			r.keepIn[e.Ref] = e
		}
		batch, err := r.call(ctx, func(b backend.Backend) (backend.FetchResult, error) {
			refetcher, ok := b.(backend.Refetcher)
			if !ok {
				return backend.FetchResult{}, nil
			}
			return refetcher.FetchRefs(ctx, request, refs)
		})
		if err == nil {
			err = r.write(ctx, j, fr, batch.Messages)
		}
		r.keepIn = nil
		if err != nil && folderOnly(err) {
			// The stubs stay as they are. The next sync tries again.
			r.closeConnection()
			return nil
		}
		if err != nil {
			return err
		}
		var problems []backend.Diagnostic
		for _, d := range batch.Diagnostics {
			if d.Reason != backend.ReasonVanished && d.Reason != backend.ReasonTooLarge {
				problems = append(problems, d)
			}
		}
		r.state.dropFetchedInFull(r.written)
		r.state.addDiagnostics(j.path, problems, r.opts.Clock(), r.written)
		if err := r.saveState(); err != nil {
			return err
		}
	}
	return nil
}
