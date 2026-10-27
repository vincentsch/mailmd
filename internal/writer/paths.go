package writer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

const unfiledFolder = "Unfiled"

// normalizedOptions holds the checked options, with an absolute root. Later
// code uses only these, never the raw values with stray spaces.
type normalizedOptions struct {
	root    string
	account string
	keepRaw bool
}

// normalizeOptions refuses an empty root or account. filepath.Abs would
// otherwise turn an empty root into the current folder.
func normalizeOptions(opts Options) (normalizedOptions, error) {
	root := trimASCIISpace(opts.Root)
	if root == "" {
		return normalizedOptions{}, invalidOptions("root is required")
	}
	account := trimASCIISpace(opts.Account)
	if account == "" {
		return normalizedOptions{}, invalidOptions("account is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return normalizedOptions{}, err
	}
	return normalizedOptions{root: abs, account: account, keepRaw: opts.KeepRaw}, nil
}

func trimASCIISpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		default:
			return false
		}
	})
}

func accountDir(account string) string {
	return mail.SafePathSegment(account)
}

// datePathParts returns the year folder and the date that starts the file
// name. They use the sender's own time zone, so a mail sent late on New
// Year's Eve is filed under the old year.
func datePathParts(t time.Time) (year string, prefix string) {
	if t.IsZero() {
		return "no-date", "no-date"
	}
	return t.Format("2006"), t.Format("2006-01-02")
}

func markdownFilename(m mail.Message, datePrefix string) (base string, filename string) {
	base = fmt.Sprintf("%s-%s--%s", datePrefix, mail.SubjectSlug(m.Subject), mail.ShortID(m.LocalID))
	return base, base + ".md"
}

// folderCopy is one folder on disk that gets a copy of a message, and the
// provider folder it stands for.
type folderCopy struct {
	key      string
	provider string
}

// diskFolderKeys turns the provider folders of a message into its folders on
// disk, sorted and without repeats. dirs replaces the usual safe name of a
// folder. When several provider folders end up in one disk folder, the copy
// stands for the first of them.
func diskFolderKeys(rawFolders []string, dirs map[string]string) []folderCopy {
	keys := map[string]string{}
	for _, raw := range rawFolders {
		if raw == "" {
			continue
		}
		key, ok := dirs[raw]
		if !ok {
			key = strings.Join(mail.NormalizeFolderPath(raw), "/")
		}
		if key == "" {
			key = unfiledFolder
		}
		if old, ok := keys[key]; !ok || raw < old {
			keys[key] = raw
		}
	}
	if len(keys) == 0 {
		keys[unfiledFolder] = ""
	}
	out := make([]folderCopy, 0, len(keys))
	for key, raw := range keys {
		out = append(out, folderCopy{key: key, provider: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// FolderDirs names the disk folders of provider folders whose usual names
// differ only in case. macOS and Windows treat such names as one folder, so
// all but the first of them get a short suffix made from the provider name.
func FolderDirs(folders []string) map[string]string {
	byLower := map[string][]string{}
	for _, raw := range folders {
		key := strings.Join(mail.NormalizeFolderPath(raw), "/")
		lower := strings.ToLower(key)
		if !slices.Contains(byLower[lower], raw) {
			byLower[lower] = append(byLower[lower], raw)
		}
	}
	dirs := map[string]string{}
	for _, group := range byLower {
		sort.Strings(group)
		for _, raw := range group[1:] {
			sum := sha256.Sum256([]byte(raw))
			dirs[raw] = strings.Join(mail.NormalizeFolderPath(raw), "/") + "-" + hex.EncodeToString(sum[:4])
		}
	}
	return dirs
}

// folderListForFrontmatter returns the provider's folder names as they are,
// for the frontmatter. diskFolderKeys makes the safe names for the disk.
func folderListForFrontmatter(rawFolders []string) []string {
	if len(rawFolders) == 0 {
		return nil
	}
	values := make([]string, 0, len(rawFolders))
	for _, folder := range rawFolders {
		if folder != "" {
			values = append(values, folder)
		}
	}
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	n := 0
	for _, value := range values {
		if n == 0 || values[n-1] != value {
			values[n] = value
			n++
		}
	}
	return values[:n]
}

// attachmentSidecarNames numbers the attachment files by their place in the
// message. An attachment without content leaves a gap in the numbers, so the
// files after it get the same names when it is downloaded later.
func attachmentSidecarNames(atts []mail.Attachment) []string {
	width := digits(len(atts))
	if width < 2 {
		width = 2
	}
	names := make([]string, len(atts))
	for i, att := range atts {
		name := mail.SafePathSegment(att.Filename)
		if strings.TrimSpace(att.Filename) == "" || name == "unnamed" {
			name = fallbackAttachmentName(att)
		}
		names[i] = fmt.Sprintf("%0*d-%s", width, i+1, name)
	}
	return names
}

// fallbackAttachmentName names an attachment that came without a file name,
// with an extension that fits its type, so it opens in the right program.
func fallbackAttachmentName(att mail.Attachment) string {
	mimeType := strings.ToLower(att.MIMEType)
	switch mimeType {
	case "message/rfc822", "message/global":
		return "forwarded-message.eml"
	case "text/calendar", "application/ics":
		return "invite.ics"
	}
	if ext, ok := extensions[mimeType]; ok {
		return "attachment" + ext
	}
	return "attachment"
}

// extensions maps common attachment types to file extensions. A fixed list
// keeps file names the same on every computer.
var extensions = map[string]string{
	"text/plain":                    ".txt",
	"text/html":                     ".html",
	"text/csv":                      ".csv",
	"text/vcard":                    ".vcf",
	"text/x-vcard":                  ".vcf",
	"application/pdf":               ".pdf",
	"application/zip":               ".zip",
	"application/json":              ".json",
	"application/xml":               ".xml",
	"application/pgp-signature":     ".asc",
	"application/pkcs7-signature":   ".p7s",
	"application/x-pkcs7-signature": ".p7s",
	"image/png":                     ".png",
	"image/jpeg":                    ".jpg",
	"image/gif":                     ".gif",
	"image/webp":                    ".webp",
	"image/svg+xml":                 ".svg",
	"audio/mpeg":                    ".mp3",
	"video/mp4":                     ".mp4",
}

func digits(n int) int {
	if n == 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

// containPath checks that target is inside root, and is not root itself.
// The paths are built from safe names already, so this is a second check.
func containPath(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return &PathError{Op: "contain", Path: target, Err: ErrUnsafePath}
	}
	return nil
}

func relPath(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
