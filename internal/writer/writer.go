package writer

import (
	"context"
	"errors"
	"fmt"

	"github.com/vincentsch/mailmd/internal/mail"
)

// Options says where and how the writer writes a message.
type Options struct {
	Root    string
	Account string
	// KeepRaw saves the original message as an .eml file next to the
	// Markdown file.
	KeepRaw bool
	// PreferText uses the plain text part of a message when it has one,
	// instead of converting its HTML part.
	PreferText bool
	// FolderDirs gives some provider folders another folder on disk. The
	// function FolderDirs makes this map.
	FolderDirs map[string]string
	// Batch collects the changed files so the caller can flush them together.
	// When it is nil, WriteMessage flushes before it returns.
	Batch *Batch
}

// WriteStatus says what writing does, or would do, to one file.
type WriteStatus string

const (
	WriteCreated   WriteStatus = "created"
	WriteUpdated   WriteStatus = "updated"
	WriteUnchanged WriteStatus = "unchanged"
	WriteSkipped   WriteStatus = "skipped"
)

// AttachmentResult describes one attachment file.
type AttachmentResult struct {
	Filename string
	Path     string // absolute path, empty when the attachment has no content
	RelPath  string // relative to Options.Root with "/", empty like Path
	SavedAs  string // relative to the Markdown file's folder, with "/"
	Status   WriteStatus
}

// FileResult describes the Markdown file of a message in one folder, with its
// attachment files.
type FileResult struct {
	// Folder is the provider folder of this copy.
	Folder      string
	Path        string
	RelPath     string
	Status      WriteStatus
	Attachments []AttachmentResult
	// RawPath and RawStatus describe the .eml file of --keep-raw.
	RawPath   string
	RawStatus WriteStatus
}

// WriteResult is what PlanMessage and WriteMessage return.
type WriteResult struct {
	LocalID    string
	AccountDir string
	Files      []FileResult
	Changed    bool
}

var (
	ErrInvalidOptions = errors.New("writer: invalid options")
	ErrPathCollision  = errors.New("writer: path collision")
	ErrUnsafePath     = errors.New("writer: unsafe path")
)

// PathError is an error with the path and the operation that failed.
type PathError struct {
	Op   string
	Path string
	Err  error
}

func (e *PathError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("writer: %s %s: %v", e.Op, e.Path, e.Err)
}

func (e *PathError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PlanMessage works out the same paths and statuses as WriteMessage, but it
// writes nothing.
func PlanMessage(ctx context.Context, opts Options, m mail.Message) (WriteResult, error) {
	plan, err := buildMessagePlan(ctx, realFileSystem{}, opts, m)
	if err != nil {
		return WriteResult{}, err
	}
	return plan.result, nil
}

// WriteMessage renders one message and writes changed Markdown and attachment
// files. Each file is written to a temporary name and renamed into place, so
// a reader never sees half a file.
func WriteMessage(ctx context.Context, opts Options, m mail.Message) (WriteResult, error) {
	if opts.Batch != nil {
		return writeMessageWithFS(ctx, realFileSystem{}, opts, m)
	}
	opts.Batch = NewBatch()
	result, err := writeMessageWithFS(ctx, realFileSystem{}, opts, m)
	if err != nil {
		return result, err
	}
	return result, opts.Batch.Sync()
}

// writeMessageWithFS plans and writes one message on fs. PlanningSession
// passes a file system in memory, and tests pass one that fails part way.
func writeMessageWithFS(ctx context.Context, fs fileSystem, opts Options, m mail.Message) (WriteResult, error) {
	plan, err := buildMessagePlan(ctx, fs, opts, m)
	if err != nil {
		return WriteResult{}, err
	}
	if err := executeMessagePlan(ctx, fs, plan); err != nil {
		return plan.result, err
	}
	return plan.result, nil
}

func invalidOptions(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOptions, fmt.Sprintf(format, args...))
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
