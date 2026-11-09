package writer

import (
	"bufio"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

// IndexEntry is one line of an account's index.jsonl. Scripts and coding
// agents can search this one file instead of opening every message.
type IndexEntry struct {
	Date string `json:"date,omitempty"`
	From string `json:"from,omitempty"`
	// To lists the recipients, like the frontmatter's to.
	To      []string `json:"to,omitempty"`
	Subject string   `json:"subject,omitempty"`
	// Folder is the provider's folder, like "INBOX" or "Work/Clients".
	Folder    string   `json:"folder"`
	Labels    []string `json:"labels,omitempty"`
	Flags     []string `json:"flags,omitempty"`
	Thread    string   `json:"thread,omitempty"`
	MessageID string   `json:"message_id,omitempty"`
	// Attachments lists the attachment names, like the frontmatter's
	// attachments.
	Attachments []string `json:"attachments,omitempty"`
	// Omitted is the size of a message that was saved with its headers
	// only, because it was larger than the size limit.
	Omitted int64 `json:"omitted_bytes,omitempty"`
	// Ref is the provider's ID of the message, like an IMAP UID. Gone is set
	// when the message was deleted on the server. Its file stays.
	Ref  string `json:"ref,omitempty"`
	Gone bool   `json:"gone,omitempty"`
	// Path is relative to the account folder and uses "/".
	Path string `json:"path"`
}

// IndexEntries returns one entry for each Markdown file of a written message.
func IndexEntries(m mail.Message, result WriteResult) []IndexEntry {
	date := ""
	if d := messageDate(m); !d.IsZero() {
		date = d.Format(time.RFC3339)
	}
	prefix := result.AccountDir + "/"
	var names []string
	for _, a := range m.Attachments {
		names = append(names, attachmentLabel(a))
	}
	var out []IndexEntry
	for _, f := range result.Files {
		rel := strings.TrimPrefix(f.RelPath, prefix)
		folder := f.Folder
		if folder == "" {
			folder = folderOf(rel)
		}
		out = append(out, IndexEntry{
			Date:        date,
			From:        mail.FormatAddress(m.From),
			To:          mail.FormatAddressList(m.To),
			Subject:     m.Subject,
			Folder:      folder,
			Labels:      m.Labels,
			Flags:       FlagNames(m.Flags),
			Thread:      m.ThreadID,
			MessageID:   m.MessageID,
			Attachments: names,
			Omitted:     m.OmittedSize,
			Ref:         m.Ref,
			Path:        rel,
		})
	}
	return out
}

// folderOf returns the folder part of a path like "Work/Clients/2026/x.md".
func folderOf(rel string) string {
	return path.Dir(path.Dir(rel))
}

// messageDate is the date the writer puts a message under. That is the Date
// header, or else the time the provider received it.
func messageDate(m mail.Message) time.Time {
	if !m.Date.IsZero() {
		return m.Date
	}
	return m.Received
}

// ReadIndexEntry builds an index entry from a Markdown file mailmd wrote, to
// rebuild an index that is lost or damaged. ok is false for a file that
// mailmd didn't write.
func ReadIndexEntry(file, rel string) (IndexEntry, bool) {
	entry, _, ok := readFrontmatter(file, rel)
	return entry, ok
}

// LocalIDOf returns the local_id of a Markdown file mailmd wrote.
func LocalIDOf(file string) (string, bool) {
	_, id, ok := readFrontmatter(file, "")
	return id, ok
}

func readFrontmatter(file, rel string) (IndexEntry, string, bool) {
	f, err := os.Open(file)
	if err != nil {
		return IndexEntry{}, "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	if !scanner.Scan() || scanner.Text() != "---" {
		return IndexEntry{}, "", false
	}
	entry := IndexEntry{Path: rel, Folder: folderOf(rel)}
	localID := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			return entry, localID, localID != ""
		}
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		raw := []byte(value)
		switch key {
		case "attachments":
			entry.Attachments, _ = parseFlowList(raw)
			continue
		case "flags":
			entry.Flags, _ = parseFlowList(raw)
			continue
		case "to":
			entry.To, _ = parseFlowList(raw)
			continue
		case "labels":
			entry.Labels, _ = parseFlowList(raw)
			continue
		case "omitted_bytes":
			entry.Omitted, _ = strconv.ParseInt(value, 10, 64)
			continue
		}
		text, _ := parseQuotedYAMLScalar(raw)
		switch key {
		case "folder":
			entry.Folder = text
		case "local_id":
			localID = text
		case "ref":
			entry.Ref = text
		case "deleted_on_server":
			entry.Gone = text != ""
		case "date":
			entry.Date = text
		case "from":
			entry.From = text
		case "subject":
			entry.Subject = text
		case "thread_id":
			entry.Thread = text
		case "message_id":
			entry.MessageID = text
		}
	}
	return IndexEntry{}, "", false
}
