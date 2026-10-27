package writer

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

// PlanningSession runs the writer against a copy of the disk in memory, for
// dry runs. Each message sees the files that the messages before it would
// write. The real disk is only read. A session is not safe for concurrent
// use.
type PlanningSession struct{ overlay *planningFS }

func NewPlanningSession() *PlanningSession {
	return &PlanningSession{overlay: &planningFS{nodes: make(map[string]*plannedNode)}}
}

func (s *PlanningSession) PlanMessage(ctx context.Context, opts Options, m mail.Message) (WriteResult, error) {
	return writeMessageWithFS(ctx, s.overlay, opts, m)
}

// PrepareDirectory creates the folders from root down to target in memory,
// with private permissions. If root already exists, its permissions stay.
func (s *PlanningSession) PrepareDirectory(ctx context.Context, root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return ErrUnsafePath
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return ErrUnsafePath
	}
	if err = containPath(root, target); err != nil {
		return err
	}
	if err = checkRequiredParentDirs(s.overlay, root, target); err != nil {
		return err
	}
	return ensureManagedDirs(ctx, s.overlay, root, target, nil)
}

type plannedNode struct {
	name     string
	mode     os.FileMode
	data     []byte
	modified time.Time
}

func (n plannedNode) Name() string       { return filepath.Base(n.name) }
func (n plannedNode) Size() int64        { return int64(len(n.data)) }
func (n plannedNode) Mode() os.FileMode  { return n.mode }
func (n plannedNode) ModTime() time.Time { return n.modified }
func (n plannedNode) IsDir() bool        { return n.mode.IsDir() }
func (n plannedNode) Sys() any           { return nil }

type planningFS struct {
	nodes    map[string]*plannedNode
	sequence uint64
}

func (f *planningFS) Lstat(path string) (os.FileInfo, error) {
	path = filepath.Clean(path)
	if n, ok := f.nodes[path]; ok {
		if n == nil {
			return nil, &os.PathError{Op: "lstat", Path: path, Err: os.ErrNotExist}
		}
		return *n, nil
	}
	return os.Lstat(path)
}
func (f *planningFS) ReadFile(path string) ([]byte, error) {
	path = filepath.Clean(path)
	if n, ok := f.nodes[path]; ok {
		if n == nil {
			return nil, os.ErrNotExist
		}
		if !n.mode.IsRegular() {
			return nil, ErrUnsafePath
		}
		return append([]byte(nil), n.data...), nil
	}
	return os.ReadFile(path)
}
func (f *planningFS) ReadDir(path string) ([]os.DirEntry, error) {
	info, err := f.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrUnsafePath
	}
	entries, err := os.ReadDir(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	merged := make(map[string]os.DirEntry)
	for _, e := range entries {
		merged[e.Name()] = e
	}
	for p, n := range f.nodes {
		if filepath.Dir(p) != filepath.Clean(path) {
			continue
		}
		if n == nil {
			delete(merged, filepath.Base(p))
		} else {
			merged[n.Name()] = fs.FileInfoToDirEntry(*n)
		}
	}
	result := make([]os.DirEntry, 0, len(merged))
	for _, e := range merged {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}
func (f *planningFS) MkdirAll(path string, perm os.FileMode) error {
	path = filepath.Clean(path)
	if info, err := f.Lstat(path); err == nil {
		if !info.IsDir() {
			return ErrUnsafePath
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := f.MkdirAll(filepath.Dir(path), perm); err != nil {
		return err
	}
	f.nodes[path] = &plannedNode{name: path, mode: os.ModeDir | perm}
	return nil
}
func (f *planningFS) Chmod(path string, perm os.FileMode) error {
	info, err := f.Lstat(path)
	if err != nil {
		return err
	}
	n := &plannedNode{name: filepath.Clean(path), mode: info.Mode().Type() | perm, modified: info.ModTime()}
	if info.Mode().IsRegular() {
		n.data, err = f.ReadFile(path)
		if err != nil {
			return err
		}
	}
	f.nodes[n.name] = n
	return nil
}
func (f *planningFS) WriteTemp(ctx context.Context, dir string, data []byte, perm os.FileMode) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	f.sequence++
	// This virtual name is never created on disk.
	path := filepath.Join(dir, ".mailmd-plan-"+time.Unix(0, int64(f.sequence)).Format("150405.000000000"))
	f.nodes[path] = &plannedNode{name: path, mode: perm, data: append([]byte(nil), data...)}
	return path, nil
}
func (f *planningFS) Rename(old, new string) error {
	data, err := f.ReadFile(old)
	if err != nil {
		return err
	}
	info, err := f.Lstat(old)
	if err != nil {
		return err
	}
	f.nodes[filepath.Clean(new)] = &plannedNode{name: filepath.Clean(new), data: data, mode: info.Mode(), modified: info.ModTime()}
	f.nodes[filepath.Clean(old)] = nil
	return nil
}
func (f *planningFS) Remove(path string) error {
	info, err := f.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := f.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return ErrUnsafePath
		}
	}
	f.nodes[filepath.Clean(path)] = nil
	return nil
}
