package imap

import (
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/backend"
)

// foldersFromList turns LIST responses into folders, sorted by path and then
// by scope key so the order is stable.
func foldersFromList(list []*imap.ListData) []backend.Folder {
	out := make([]backend.Folder, 0, len(list))
	for _, data := range list {
		if data == nil {
			continue
		}
		delimiter := ""
		if data.Delim != 0 {
			delimiter = string(data.Delim)
		}
		folder := backend.Folder{
			Path:      displayPath(data.Mailbox, delimiter),
			Fetchable: !hasAttr(data.Attrs, imap.MailboxAttrNoSelect) && !hasAttr(data.Attrs, imap.MailboxAttrNonExistent),
			Role:      roleOf(data.Mailbox, delimiter, data.Attrs),
		}
		if folder.Fetchable {
			folder.ScopeKey = encodeScopeKey(data.Mailbox, delimiter, delimiter != "")
		}
		out = append(out, folder)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].ScopeKey < out[j].ScopeKey
	})
	return out
}

// displayPath uses "/" between levels, whatever the server's delimiter is.
func displayPath(mailbox, delimiter string) string {
	path := canonicalMailbox(mailbox)
	if delimiter != "" && delimiter != "/" {
		path = strings.ReplaceAll(path, delimiter, "/")
	}
	return path
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, attr := range attrs {
		if strings.EqualFold(string(attr), string(want)) {
			return true
		}
	}
	return false
}

// roleAttrs maps the special-use attributes of RFC 6154 and RFC 8457 to
// folder roles.
var roleAttrs = []struct {
	attr imap.MailboxAttr
	role backend.Role
}{
	{imap.MailboxAttrAll, backend.RoleAll},
	{imap.MailboxAttrJunk, backend.RoleJunk},
	{imap.MailboxAttrTrash, backend.RoleTrash},
	{imap.MailboxAttrSent, backend.RoleSent},
	{imap.MailboxAttrDrafts, backend.RoleDrafts},
	{imap.MailboxAttrArchive, backend.RoleArchive},
	{imap.MailboxAttrFlagged, backend.RoleFlagged},
	{imap.MailboxAttrImportant, backend.RoleImportant},
}

// roleNames recognizes common junk and trash folders by name, for servers
// that do not send special-use attributes. Names are compared in lower case.
var roleNames = map[string]backend.Role{
	"spam":               backend.RoleJunk,
	"junk":               backend.RoleJunk,
	"junk e-mail":        backend.RoleJunk,
	"junk email":         backend.RoleJunk,
	"junk-e-mail":        backend.RoleJunk,
	"bulk mail":          backend.RoleJunk,
	"spamverdacht":       backend.RoleJunk,
	"trash":              backend.RoleTrash,
	"deleted items":      backend.RoleTrash,
	"deleted messages":   backend.RoleTrash,
	"bin":                backend.RoleTrash,
	"papierkorb":         backend.RoleTrash,
	"gelöschte elemente": backend.RoleTrash,
}

func roleOf(mailbox, delimiter string, attrs []imap.MailboxAttr) backend.Role {
	if strings.EqualFold(mailbox, "INBOX") {
		return backend.RoleInbox
	}
	for _, r := range roleAttrs {
		if hasAttr(attrs, r.attr) {
			return r.role
		}
	}
	name := mailbox
	if delimiter != "" {
		if i := strings.LastIndex(name, delimiter); i >= 0 {
			name = name[i+len(delimiter):]
		}
	}
	return roleNames[strings.ToLower(name)]
}
