package writer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/privatefs"
)

// messagePlan holds everything that writing one message will do: the bytes
// and paths of each file next to the result. WriteMessage then does exactly
// what PlanMessage reported.
type messagePlan struct {
	root       string
	batch      *Batch
	repairDirs bool
	// hasPayload is set when at least one attachment has data, so the
	// attachments folder is needed.
	hasPayload bool
	result     WriteResult
	copies     []copyPlan
}

// copyPlan is the Markdown file of a message in one folder, with the
// attachment files next to it.
type copyPlan struct {
	// folder is the provider folder this copy stands for.
	folder     string
	dir        string
	sidecarDir string
	markdown   filePlan
	// raw is the original message. It is saved next to the Markdown file with
	// --keep-raw, and always for a message that could not be read.
	raw         *filePlan
	attachments []attachmentPlan
}

// filePlan holds the bytes for one file and its WriteStatus.
type filePlan struct {
	path   string
	rel    string
	data   []byte
	status WriteStatus
}

// attachmentPlan is like AttachmentResult, plus the content. An attachment
// without content (hasPayload false) gets no path.
type attachmentPlan struct {
	filename   string
	path       string
	rel        string
	savedAs    string
	data       []byte
	status     WriteStatus
	hasPayload bool
}

// buildMessagePlan works out the files of a message for PlanMessage and
// WriteMessage. It only reads from the disk, so all checks happen before
// anything is written.
func buildMessagePlan(ctx context.Context, fs fileSystem, opts Options, m mail.Message) (messagePlan, error) {
	nopts, err := normalizeOptions(opts)
	if err != nil {
		return messagePlan{}, err
	}
	if err := checkContext(ctx); err != nil {
		return messagePlan{}, err
	}

	ensured, err := mail.EnsureLocalID(m)
	if err != nil {
		return messagePlan{}, err
	}
	if err := checkContext(ctx); err != nil {
		return messagePlan{}, err
	}

	date := messageDate(ensured)
	year, datePrefix := datePathParts(date)
	account := accountDir(nopts.account)
	base, markdownName := markdownFilename(ensured, datePrefix)
	sidecarDirName := base + ".attachments"
	sidecarNames := attachmentSidecarNames(ensured.Attachments)
	savedAs := make([]string, len(ensured.Attachments))
	cids := map[string]string{}
	hasPayload := false
	for i, att := range ensured.Attachments {
		if att.Data != nil {
			hasPayload = true
			savedAs[i] = sidecarDirName + "/" + sidecarNames[i]
			if att.ContentID != "" {
				cids[normalizeCID(att.ContentID)] = savedAs[i]
			}
		}
	}

	body, err := renderBody(ctx, ensured, cids, opts.PreferText)
	if err != nil {
		return messagePlan{}, err
	}
	// A message mailmd could not read is always saved as an .eml file, so
	// nothing of it is lost.
	keepRaw := (nopts.keepRaw || ensured.ParseError != "") && len(ensured.Raw) > 0
	rawName := ""
	if keepRaw {
		rawName = base + ".eml"
	}
	markdownBytes := renderMarkdownBytes(ensured, date, savedAs, body, rawName)

	folders := diskFolderKeys(ensured.Folders, opts.FolderDirs)
	plan := messagePlan{root: nopts.root, batch: opts.Batch, hasPayload: hasPayload}
	for _, folder := range folders {
		if err := checkContext(ctx); err != nil {
			return messagePlan{}, err
		}
		copyDir := filepath.Join(nopts.root, account, filepath.FromSlash(folder.key), year)
		markdownPath := filepath.Join(copyDir, markdownName)
		sidecarDir := filepath.Join(copyDir, sidecarDirName)
		if err := containPath(nopts.root, markdownPath); err != nil {
			return messagePlan{}, err
		}
		if err := containPath(nopts.root, sidecarDir); err != nil {
			return messagePlan{}, err
		}
		markdownRel, err := relPath(nopts.root, markdownPath)
		if err != nil {
			return messagePlan{}, err
		}
		copy := copyPlan{
			folder:     folder.provider,
			dir:        copyDir,
			sidecarDir: sidecarDir,
			markdown: filePlan{
				path: filepath.Clean(markdownPath),
				rel:  markdownRel,
				data: markdownBytes,
			},
		}
		if keepRaw {
			rawPath := filepath.Join(copyDir, rawName)
			rawRel, err := relPath(nopts.root, rawPath)
			if err != nil {
				return messagePlan{}, err
			}
			copy.raw = &filePlan{path: filepath.Clean(rawPath), rel: rawRel, data: ensured.Raw}
		}
		for i, att := range ensured.Attachments {
			ap := attachmentPlan{
				filename: att.Filename,
				status:   WriteSkipped,
			}
			if att.Data != nil {
				sidecarPath := filepath.Join(sidecarDir, sidecarNames[i])
				if err := containPath(nopts.root, sidecarPath); err != nil {
					return messagePlan{}, err
				}
				sidecarRel, err := relPath(nopts.root, sidecarPath)
				if err != nil {
					return messagePlan{}, err
				}
				ap.path = filepath.Clean(sidecarPath)
				ap.rel = sidecarRel
				ap.savedAs = savedAs[i]
				ap.data = att.Data
				ap.hasPayload = true
			}
			copy.attachments = append(copy.attachments, ap)
		}
		plan.copies = append(plan.copies, copy)
	}

	if err := plan.determineStatuses(ctx, fs, ensured.LocalID); err != nil {
		return messagePlan{}, err
	}
	plan.result = plan.toResult(ensured.LocalID, account)
	return plan, nil
}

// determineStatuses checks each file of each copy against the disk.
// WriteMessage writes nothing until all of them passed.
func (p *messagePlan) determineStatuses(ctx context.Context, fs fileSystem, localID string) error {
	for ci := range p.copies {
		if err := checkContext(ctx); err != nil {
			return err
		}
		copy := &p.copies[ci]
		if err := checkRequiredParentDirs(fs, p.root, copy.dir); err != nil {
			return err
		}
		repair, err := directoriesNeedRepair(fs, p.root, copy.dir)
		if err != nil {
			return err
		}
		p.repairDirs = p.repairDirs || repair
		if p.hasPayload {
			if err := checkSidecarDir(fs, copy.sidecarDir); err != nil {
				return err
			}
			repair, err := directoriesNeedRepair(fs, p.root, copy.sidecarDir)
			if err != nil {
				return err
			}
			p.repairDirs = p.repairDirs || repair
		}

		status, err := statusMarkdown(fs, copy.markdown.path, copy.markdown.data, localID)
		if err != nil {
			return err
		}
		copy.markdown.status = status
		if copy.raw != nil {
			if copy.raw.status, err = statusSidecar(fs, copy.raw.path, copy.raw.data); err != nil {
				return err
			}
		}

		for ai := range copy.attachments {
			if err := checkContext(ctx); err != nil {
				return err
			}
			att := &copy.attachments[ai]
			if !att.hasPayload {
				continue
			}
			status, err := statusSidecar(fs, att.path, att.data)
			if err != nil {
				return err
			}
			att.status = status
		}
	}
	return nil
}

// toResult turns the plan into the WriteResult, without the file contents.
// Changed is set when any file gets created or updated.
func (p messagePlan) toResult(localID, account string) WriteResult {
	result := WriteResult{
		Changed:    p.repairDirs,
		LocalID:    localID,
		AccountDir: account,
		Files:      make([]FileResult, 0, len(p.copies)),
	}
	for _, copy := range p.copies {
		file := FileResult{
			Folder:      copy.folder,
			Path:        copy.markdown.path,
			RelPath:     copy.markdown.rel,
			Status:      copy.markdown.status,
			Attachments: make([]AttachmentResult, 0, len(copy.attachments)),
		}
		if changedStatus(copy.markdown.status) {
			result.Changed = true
		}
		if copy.raw != nil {
			file.RawPath, file.RawStatus = copy.raw.path, copy.raw.status
			if changedStatus(copy.raw.status) {
				result.Changed = true
			}
		}
		for _, att := range copy.attachments {
			file.Attachments = append(file.Attachments, AttachmentResult{
				Filename: att.filename,
				Path:     att.path,
				RelPath:  att.rel,
				SavedAs:  att.savedAs,
				Status:   att.status,
			})
			if changedStatus(att.status) {
				result.Changed = true
			}
		}
		result.Files = append(result.Files, file)
	}
	return result
}

func changedStatus(status WriteStatus) bool {
	return status == WriteCreated || status == WriteUpdated
}

// executeMessagePlan writes only the files that are new or changed. It leaves
// unchanged files alone, so running it again doesn't change their times. It
// never deletes a file, and files someone put next to a message stay where
// they are.
func executeMessagePlan(ctx context.Context, fs fileSystem, plan messagePlan) error {
	for _, copy := range plan.copies {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := ensureManagedDirs(ctx, fs, plan.root, copy.dir, plan.batch); err != nil {
			return err
		}
		if plan.hasPayload {
			if err := ensureManagedDirs(ctx, fs, plan.root, copy.sidecarDir, plan.batch); err != nil {
				return err
			}
		}
		for _, att := range copy.attachments {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if !att.hasPayload {
				continue
			}
			if err := writeManagedFile(ctx, fs, att.path, att.data, att.status, plan.batch); err != nil {
				return err
			}
		}
		if copy.raw != nil {
			if err := writeManagedFile(ctx, fs, copy.raw.path, copy.raw.data, copy.raw.status, plan.batch); err != nil {
				return err
			}
		}
		if err := writeManagedFile(ctx, fs, copy.markdown.path, copy.markdown.data, copy.markdown.status, plan.batch); err != nil {
			return err
		}
	}
	return nil
}

func writeManagedFile(ctx context.Context, fs fileSystem, path string, data []byte, status WriteStatus, batch *Batch) error {
	if !changedStatus(status) {
		return nil
	}
	current, err := fs.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return &PathError{Op: "open", Path: path, Err: err}
	}
	if err == nil && bytes.Equal(current, data) {
		// Only the permissions differ.
		if err := fs.Chmod(path, 0o600); err != nil {
			return &PathError{Op: "chmod", Path: path, Err: err}
		}
		return nil
	}
	return atomicWrite(ctx, fs, path, data, batch)
}

// atomicWrite writes a temporary file in the same folder and renames it to
// path. If Ctrl-C comes before the rename, the temporary file is removed and
// path stays as it was.
func atomicWrite(ctx context.Context, fs fileSystem, path string, data []byte, batch *Batch) error {
	tmp, err := fs.WriteTemp(ctx, filepath.Dir(path), data, 0o600)
	if err != nil {
		var perr *PathError
		if errors.As(err, &perr) {
			return err
		}
		return &PathError{Op: "write", Path: path, Err: err}
	}
	if err := checkContext(ctx); err != nil {
		_ = fs.Remove(tmp)
		return err
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return &PathError{Op: "rename", Path: path, Err: err}
	}
	batch.addFile(path)
	batch.addDir(filepath.Dir(path))
	return nil
}

// checkRequiredParentDirs refuses symbolic links and files where the
// folders above targetDir should be. Missing folders are fine, because
// ensureManagedDirs creates them later.
func checkRequiredParentDirs(fs fileSystem, root, targetDir string) error {
	root = filepath.Clean(root)
	for p := root; ; p = filepath.Dir(p) {
		info, err := fs.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return &PathError{Op: "open", Path: p, Err: err}
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 && p != root && privatefs.TrustedLink(info) {
			// A link above the root that only root can change, like /var
			// on macOS.
		} else if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return &PathError{Op: "open", Path: p, Err: ErrUnsafePath}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	targetDir = filepath.Clean(targetDir)
	if err := containPath(root, targetDir); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, targetDir)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, part := range splitRelPath(rel) {
		current = filepath.Join(current, part)
		info, err := fs.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return &PathError{Op: "open", Path: current, Err: err}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &PathError{Op: "open", Path: current, Err: ErrUnsafePath}
		}
	}
	return nil
}

func splitRelPath(rel string) []string {
	return strings.FieldsFunc(rel, func(r rune) bool {
		return r == os.PathSeparator
	})
}

// checkSidecarDir rejects an attachments folder that is a symbolic link or
// not a folder at all.
func checkSidecarDir(fs fileSystem, path string) error {
	info, err := fs.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return &PathError{Op: "open", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	return nil
}

// statusMarkdown compares the planned bytes with the file on disk.
func statusMarkdown(fs fileSystem, path string, data []byte, localID string) (WriteStatus, error) {
	info, err := fs.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WriteCreated, nil
		}
		return "", &PathError{Op: "open", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	current, err := fs.ReadFile(path)
	if err != nil {
		return "", &PathError{Op: "open", Path: path, Err: err}
	}
	if bytes.Equal(current, data) && hasPrivateMode(info, 0o600) {
		return WriteUnchanged, nil
	}
	if !ownedFile(current, localID) {
		return "", &PathError{Op: "collision", Path: path, Err: ErrPathCollision}
	}
	return WriteUpdated, nil
}

// ownedFile reports whether mailmd may replace the file at a message's path.
// That is the case for a file with this message's local_id. It is also the
// case for what a crash can leave behind before the batch is flushed, a file
// that is empty or cut off. Zero bytes count as empty. Anything else, like a
// file the user put there, is not ours.
func ownedFile(content []byte, localID string) bool {
	if len(bytes.Trim(content, "\x00")) == 0 {
		return true
	}
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return false
	}
	lines := bytes.Split(content[4:], []byte{'\n'})
	for i, line := range lines {
		if bytes.Equal(line, []byte("---")) {
			// The frontmatter is complete but has no local_id.
			return false
		}
		value, ok := bytes.CutPrefix(line, []byte("local_id: "))
		if !ok {
			continue
		}
		if id, ok := parseQuotedYAMLScalar(value); ok {
			return id == localID
		}
		// A line cut off at the end of the file is a crash leftover.
		return i == len(lines)-1
	}
	// The file ends inside the frontmatter, so it was cut off.
	return true
}

// statusSidecar compares an attachment file with its content. Attachment
// files have no local_id, so a file with other content gets overwritten.
func statusSidecar(fs fileSystem, path string, data []byte) (WriteStatus, error) {
	info, err := fs.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WriteCreated, nil
		}
		return "", &PathError{Op: "open", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", &PathError{Op: "open", Path: path, Err: ErrUnsafePath}
	}
	current, err := fs.ReadFile(path)
	if err != nil {
		return "", &PathError{Op: "open", Path: path, Err: err}
	}
	if bytes.Equal(current, data) && hasPrivateMode(info, 0o600) {
		return WriteUnchanged, nil
	}
	return WriteUpdated, nil
}
