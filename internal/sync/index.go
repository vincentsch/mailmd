package sync

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/privatefs"
	"github.com/vincentsch/mailmd/internal/writer"
)

// IndexFile is the name of the index in each account folder.
const IndexFile = "index.jsonl"

// indexPath returns the path of the index after the same checks as for the
// other files of the archive. No folder on the way may be a symbolic link,
// and the index must be a regular file. With repair, a file that others can
// read is made private again.
func indexPath(dir string, repair bool) (string, error) {
	path := filepath.Join(dir, IndexFile)
	if privatefs.CheckPath(path) != nil {
		return "", privatefs.ErrIntegrity
	}
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return path, nil
	case err != nil:
		return "", err
	case !info.Mode().IsRegular():
		return "", privatefs.ErrIntegrity
	case repair && !privatefs.HasMode(info.Mode(), 0o600):
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
	}
	return path, nil
}

// appendIndex adds entries to the index and records the file in the batch,
// so the entries reach the disk together with the messages.
func appendIndex(dir string, entries []writer.IndexEntry, batch *writer.Batch) error {
	if len(entries) == 0 {
		return nil
	}
	buf, err := encodeEntries(entries)
	if err != nil {
		return err
	}
	path, err := indexPath(dir, true)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	batch.AddFile(path)
	return nil
}

// encodeEntries writes one JSON object per line. Characters like < and >
// stay as they are, so the file is easy to grep.
func encodeEntries(entries []writer.IndexEntry) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return nil, err
		}
	}
	return &buf, nil
}

// readIndex reads the index. A later line for the same path replaces an
// earlier one. damaged is set when a line is not a complete entry, as after a
// crash in the middle of a write. The caller then builds the index again from
// the Markdown files.
func readIndex(dir string) (entries map[string]writer.IndexEntry, damaged bool, err error) {
	path, err := indexPath(dir, false)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	entries = map[string]writer.IndexEntry{}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e writer.IndexEntry
			complete := line[len(line)-1] == '\n'
			if !complete || json.Unmarshal(line, &e) != nil || e.Path == "" {
				damaged = true
			} else {
				entries[e.Path] = e
			}
		}
		if err == io.EOF {
			return entries, damaged, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
}

// compactIndex rewrites the index with one line per file, newest first.
func compactIndex(dir string, entries map[string]writer.IndexEntry) error {
	list := make([]writer.IndexEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := entryTime(list[i]), entryTime(list[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return list[i].Path < list[j].Path
	})
	buf, err := encodeEntries(list)
	if err != nil {
		return err
	}
	path, err := indexPath(dir, true)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, buf.Bytes()) {
		return nil
	}
	return privatefs.WriteFile(path, buf.Bytes(), privatefs.Hooks{})
}

func entryTime(e writer.IndexEntry) time.Time {
	t, _ := time.Parse(time.RFC3339, e.Date)
	return t
}

// rebuildIndex reads all Markdown files of the archive. A sync uses it when
// the index is damaged, or missing from an archive that has mail. The files
// hold every field of the index, so nothing is lost.
func rebuildIndex(dir string) (map[string]writer.IndexEntry, error) {
	entries := map[string]writer.IndexEntry{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (d.Name() == ".mailmd" || strings.HasSuffix(d.Name(), ".attachments")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if e, ok := writer.ReadIndexEntry(path, rel); ok {
			entries[rel] = e
		}
		return nil
	})
	return entries, err
}
