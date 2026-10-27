package gmail

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

// labelKind says what a Gmail label means for the archive.
type labelKind int

const (
	labelExcluded labelKind = iota
	labelState
	labelFolder
)

// systemLabel says what mailmd does with one of Gmail's own labels.
type systemLabel struct {
	kind labelKind
	path string
	role mdbackend.Role
}

// systemLabels lists Gmail's own labels by ID. Their meaning doesn't depend
// on the name that the API returns.
var systemLabels = map[string]systemLabel{
	"INBOX":               {kind: labelFolder, path: "Inbox", role: mdbackend.RoleInbox},
	"SENT":                {kind: labelFolder, path: "Sent", role: mdbackend.RoleSent},
	"DRAFT":               {kind: labelFolder, path: "Drafts", role: mdbackend.RoleDrafts},
	"SPAM":                {kind: labelFolder, path: "Spam", role: mdbackend.RoleJunk},
	"TRASH":               {kind: labelFolder, path: "Trash", role: mdbackend.RoleTrash},
	"IMPORTANT":           {kind: labelFolder, path: "Important", role: mdbackend.RoleImportant},
	"CATEGORY_PERSONAL":   {kind: labelFolder, path: "Categories/Personal"},
	"CATEGORY_SOCIAL":     {kind: labelFolder, path: "Categories/Social"},
	"CATEGORY_PROMOTIONS": {kind: labelFolder, path: "Categories/Promotions"},
	"CATEGORY_UPDATES":    {kind: labelFolder, path: "Categories/Updates"},
	"CATEGORY_FORUMS":     {kind: labelFolder, path: "Categories/Forums"},
	"UNREAD":              {kind: labelState},
	"STARRED":             {kind: labelState},
	"CHAT":                {kind: labelExcluded},
}

// labelRecord is one label after mailmd sorted it into a kind. path is the
// label's name in Gmail, and final is the folder path mailmd uses, made
// unique. When several labels have the same path, the one with the lowest
// priority gets the path without a suffix.
type labelRecord struct {
	id       string
	path     string
	final    string
	kind     labelKind
	priority int
	role     mdbackend.Role
}

// labelCatalog holds the labels of one account. Folders and the messages use
// the same catalog, so they agree on folder paths. final is set only for
// labels that are folders.
type labelCatalog struct {
	records map[string]labelRecord
	listed  []labelRecord
}

// buildLabelCatalog sorts the labels into kinds and makes the folder paths
// unique.
func buildLabelCatalog(labels []gmailLabel) labelCatalog {
	records := make([]labelRecord, 0, len(labels))
	seen := map[string]struct{}{}
	for _, label := range labels {
		if label.ID == "" {
			continue
		}
		if _, ok := seen[label.ID]; ok {
			continue
		}
		seen[label.ID] = struct{}{}
		rec := recordForListedLabel(label)
		if rec.kind == labelFolder {
			records = append(records, rec)
		} else {
			rec.final = rec.path
		}
		seen[label.ID] = struct{}{}
	}

	finalizeLabelPaths(records)
	byID := make(map[string]labelRecord, len(labels)+len(systemLabels))
	for _, label := range labels {
		if label.ID == "" {
			continue
		}
		rec := recordForListedLabel(label)
		if rec.kind == labelFolder {
			// Use the unique path from above, so a message's folder is the
			// same as the one Folders lists.
			for _, folderRec := range records {
				if folderRec.id == label.ID {
					rec.final = folderRec.final
					break
				}
			}
		}
		byID[label.ID] = rec
	}
	return labelCatalog{records: byID, listed: records}
}

// recordForListedLabel sorts one label into a kind, before the paths are made
// unique.
func recordForListedLabel(label gmailLabel) labelRecord {
	if sys, ok := systemLabels[label.ID]; ok {
		return labelRecord{
			id:       label.ID,
			path:     sys.path,
			kind:     sys.kind,
			priority: 0,
			role:     sys.role,
		}
	}
	if strings.EqualFold(label.Type, "system") {
		return labelRecord{
			id:       label.ID,
			path:     displayPathOrFallback(label.ID, label.Name),
			kind:     labelFolder,
			priority: 1,
		}
	}
	return labelRecord{
		id:       label.ID,
		path:     displayPathOrFallback(label.ID, label.Name),
		kind:     labelFolder,
		priority: 2,
	}
}

// displayPathOrFallback gives a label without a name a path made from a hash
// of its ID. The path is the same on each sync and doesn't show the ID.
func displayPathOrFallback(labelID, name string) string {
	if name != "" {
		return name
	}
	return "Unnamed Label (" + labelHash(labelID) + ")"
}

// finalizeLabelPaths sets final on each record. When labels share a path, the
// first by priority gets the path as it is. The others get a suffix made from
// a hash of their ID.
func finalizeLabelPaths(records []labelRecord) {
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		return a.id < b.id
	})

	used := map[string]string{}
	for i := 0; i < len(records); {
		j := i + 1
		for j < len(records) && records[j].path == records[i].path {
			j++
		}
		group := records[i:j]
		sort.Slice(group, func(a, b int) bool {
			if group[a].priority != group[b].priority {
				return group[a].priority < group[b].priority
			}
			return group[a].id < group[b].id
		})
		for k := range group {
			candidate := group[k].path
			if k > 0 {
				candidate = appendLabelSuffix(candidate, labelHash(group[k].id))
			}
			for {
				owner, ok := used[candidate]
				if !ok || owner == group[k].id {
					break
				}
				candidate = appendLabelSuffix(candidate, labelHash(group[k].id))
			}
			group[k].final = candidate
			used[candidate] = group[k].id
		}
		i = j
	}
}

func appendLabelSuffix(path, hash string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return "Unnamed Label (" + hash + ")"
	}
	parts[len(parts)-1] = parts[len(parts)-1] + " (label " + hash + ")"
	return strings.Join(parts, "/")
}

// folders lists the labels that are folders. Labels like UNREAD only set
// flags on messages.
func (c labelCatalog) folders() []mdbackend.Folder {
	records := append([]labelRecord(nil), c.listed...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].final != records[j].final {
			return records[i].final < records[j].final
		}
		return records[i].id < records[j].id
	})
	out := make([]mdbackend.Folder, 0, len(records)+1)
	archive := true
	for _, rec := range records {
		out = append(out, mdbackend.Folder{
			Path:      rec.final,
			Fetchable: true,
			ScopeKey:  accountScopeKey,
			Role:      rec.role,
		})
		archive = archive && rec.final != archiveFolder
	}
	// Mail without a folder label, like archived mail, is filed under
	// Archive. Listing it lets --folder and --exclude name it.
	if archive {
		out = append(out, mdbackend.Folder{Path: archiveFolder, Fetchable: true, ScopeKey: accountScopeKey, Role: mdbackend.RoleArchive})
		sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	}
	return out
}

// archiveFolder holds the messages that have none of the labels mailmd
// files mail under.
const archiveFolder = "Archive"

// labelsAndFlags turns a message's label IDs into its labels and flags, and
// picks the one folder for its file. The folder is the first of these that
// the message has:
//
//   - Inbox, Sent, Drafts, Spam or Trash, in this order
//   - a label of the user
//   - a category
//
// A message without any of them was archived and goes to Archive.
func (c labelCatalog) labelsAndFlags(labelIDs []string) (folder string, labels []string, flags mdmail.Flags) {
	flags = mdmail.Flags{Seen: true}
	set := map[string]labelRecord{}
	for _, id := range labelIDs {
		switch id {
		case "UNREAD":
			flags.Seen = false
		case "STARRED":
			flags.Flagged = true
		case "DRAFT":
			flags.Draft = true
		}
		rec, ok := c.records[id]
		if !ok {
			rec = fallbackRecordForMessageLabel(id)
		}
		if rec.kind != labelFolder {
			continue
		}
		path := rec.final
		if path == "" {
			path = rec.path
		}
		if path != "" {
			set[path] = rec
		}
	}
	for path := range set {
		labels = append(labels, path)
	}
	sort.Strings(labels)
	for _, id := range []string{"INBOX", "SENT", "DRAFT", "SPAM", "TRASH"} {
		for _, path := range labels {
			if set[path].id == id {
				return path, labels, flags
			}
		}
	}
	for _, path := range labels {
		if rec := set[path]; rec.priority > 0 {
			return path, labels, flags
		}
	}
	for _, path := range labels {
		if strings.HasPrefix(set[path].id, "CATEGORY_") {
			return path, labels, flags
		}
	}
	return archiveFolder, labels, flags
}

// fallbackRecordForMessageLabel makes a record for a label that a message has
// but that the label list didn't include.
func fallbackRecordForMessageLabel(id string) labelRecord {
	if sys, ok := systemLabels[id]; ok {
		return labelRecord{id: id, path: sys.path, final: sys.path, kind: sys.kind, priority: 0}
	}
	path := displayPathOrFallback(id, "")
	return labelRecord{id: id, path: path, final: path, kind: labelFolder, priority: 2}
}

// labelHash makes the short suffix that tells apart labels with the same
// path. It has no security purpose.
func labelHash(labelID string) string {
	sum := sha256.Sum256([]byte("mailmd-gmail-label-v1\x00" + labelID))
	return hex.EncodeToString(sum[:])[:10]
}
