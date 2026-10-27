package writer

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
)

// UpdateFlags changes the flags line of a Markdown file mailmd wrote, when
// the flags changed on the server. The rest of the file stays as it is.
func UpdateFlags(ctx context.Context, path string, flags []string, batch *Batch) error {
	line := ""
	if len(flags) > 0 {
		var b strings.Builder
		writeStringList(&b, "flags", flags)
		line = b.String()
	}
	return updateField(ctx, path, "flags", line, batch)
}

// MarkDeleted adds deleted_on_server with the day the deletion was noticed,
// like "2026-10-26". An empty day removes the field, for a message that is
// back on the server.
func MarkDeleted(ctx context.Context, path, day string, batch *Batch) error {
	line := ""
	if day != "" {
		var b strings.Builder
		writeScalar(&b, "deleted_on_server", day)
		line = b.String()
	}
	return updateField(ctx, path, "deleted_on_server", line, batch)
}

// SetThread changes the thread_id of a Markdown file mailmd wrote.
func SetThread(ctx context.Context, path, thread string, batch *Batch) error {
	var b strings.Builder
	writeScalar(&b, "thread_id", thread)
	return updateField(ctx, path, "thread_id", b.String(), batch)
}

// updateField changes one field of a file mailmd wrote. It returns an error
// that wraps ErrPathCollision for a file that is not a regular Markdown
// file with frontmatter, like one you replaced with your own text.
func updateField(ctx context.Context, path, key, line string, batch *Batch) error {
	info, err := os.Lstat(path)
	if err != nil {
		return &PathError{Op: "open", Path: path, Err: err}
	}
	if !info.Mode().IsRegular() {
		return &PathError{Op: "open", Path: path, Err: ErrPathCollision}
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return &PathError{Op: "open", Path: path, Err: err}
	}
	updated, ok := setField(current, key, strings.TrimSuffix(line, "\n"))
	if !ok {
		return &PathError{Op: "update", Path: path, Err: ErrPathCollision}
	}
	if bytes.Equal(updated, current) {
		return nil
	}
	return atomicWrite(ctx, realFileSystem{}, path, updated, batch)
}

// setField replaces the line of one frontmatter field, adds it in its place
// in frontmatterOrder, or removes it when line is empty.
func setField(content []byte, key, line string) ([]byte, bool) {
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return nil, false
	}
	end := bytes.Index(content[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, false
	}
	rank := slices.Index(frontmatterOrder, key)
	front := strings.Split(string(content[4:4+end]), "\n")
	var out []string
	placed := false
	for _, l := range front {
		name, _, _ := strings.Cut(l, ": ")
		switch {
		case name == key:
			if line != "" && !placed {
				out = append(out, line)
			}
			placed = true
		case !placed && slices.Index(frontmatterOrder, name) > rank:
			if line != "" {
				out = append(out, line)
			}
			placed = true
			out = append(out, l)
		default:
			out = append(out, l)
		}
	}
	if !placed && line != "" {
		out = append(out, line)
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.WriteString(strings.Join(out, "\n"))
	b.Write(content[4+end:])
	return b.Bytes(), true
}
