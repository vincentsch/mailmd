package writer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/vincentsch/mailmd/internal/privatefs"
)

// Batch collects the files and folders that one sync batch changed, and Sync
// flushes them to disk together. A sync moves its position forward only after
// Sync returns. So after a crash, the next run fetches and writes the batch
// again, and no message goes missing.
//
// Flushing once per batch, and many files at a time, is much faster than
// flushing after each file, because the file system can combine the work.
type Batch struct {
	mu    sync.Mutex
	files map[string]bool
	dirs  map[string]bool
}

// NewBatch returns an empty batch.
func NewBatch() *Batch {
	return &Batch{files: map[string]bool{}, dirs: map[string]bool{}}
}

// AddFile records a file that the caller changed itself.
func (b *Batch) AddFile(path string) {
	b.addFile(path)
	b.addDir(filepath.Dir(path))
}

func (b *Batch) addFile(path string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.files[path] = true
	b.mu.Unlock()
}

func (b *Batch) addDir(path string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.dirs[path] = true
	b.mu.Unlock()
}

// syncWorkers is how many files are flushed at the same time.
const syncWorkers = 16

// Sync flushes all recorded files, then all recorded folders, and empties
// the batch.
func (b *Batch) Sync() error {
	b.mu.Lock()
	files, dirs := sortedKeys(b.files), sortedKeys(b.dirs)
	b.files, b.dirs = map[string]bool{}, map[string]bool{}
	b.mu.Unlock()
	if err := syncAll(files, privatefs.SyncFile); err != nil {
		return err
	}
	return syncAll(dirs, privatefs.SyncDir)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func syncAll(paths []string, flush func(string) error) error {
	jobs := make(chan string)
	errs := make(chan error, len(paths))
	var wg sync.WaitGroup
	for range min(syncWorkers, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if err := flush(path); err != nil && !os.IsNotExist(err) {
					errs <- &PathError{Op: "sync", Path: path, Err: err}
				}
			}
		}()
	}
	for _, path := range paths {
		jobs <- path
	}
	close(jobs)
	wg.Wait()
	close(errs)
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}
