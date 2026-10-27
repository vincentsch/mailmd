package imaptest

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

const (
	scopeKeyPrefix = "imap-v1:"
	scopeKeyMagic  = "mailmd-imap-scope-v1\x00"
)

// FolderSeed describes one mailbox of the test server.
type FolderSeed struct {
	Name            string
	Delimiter       string
	Attributes      []string
	UIDValidity     uint32
	Messages        []MessageSeed
	OmitUIDNext     bool
	OmitUIDValidity bool
	// SelectError makes EXAMINE of this mailbox fail with this response.
	SelectError *imap.Error
}

// MessageSeed is one raw RFC 822 message in a mailbox of the test server.
type MessageSeed struct {
	ReportedSize *uint32
	OmitSize     bool
	// OmitBody makes FETCH leave out the message content, like a broken
	// server.
	OmitBody     bool
	UID          uint32
	Flags        []string
	InternalDate time.Time
	Raw          []byte
	Expected     ExpectedMessage
}

// ExpectedFolder is the folder that a test expects the backend to list.
type ExpectedFolder struct {
	Path      string
	Fetchable bool
	ScopeKey  string
}

// SeedFolders returns a new copy of the default mailboxes. They are the same
// on each call.
func SeedFolders() []FolderSeed {
	return []FolderSeed{
		{Name: "INBOX", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 101, Messages: inboxMessages()},
		{Name: "Work", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasChildren)}, UIDValidity: 201, Messages: workMessages()},
		{Name: "Work/Projects", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 202, Messages: projectMessages()},
		{Name: "Отчёты", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 301, Messages: reportMessages()},
		{Name: "[Gmail]", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrNoSelect), string(imap.MailboxAttrHasChildren)}},
		{Name: "[Gmail]/All Mail", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrAll), string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 401, Messages: allMailMessages()},
		{Name: "[Gmail]/Sent Mail", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrSent), string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 402},
		{Name: "Foo.Bar", Delimiter: ".", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 501, Messages: collisionMessages(true)},
		{Name: "Foo/Bar", Delimiter: ".", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 502, Messages: collisionMessages(false)},
		{Name: "Flat", Delimiter: "", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 601, Messages: flatMessages()},
		{Name: "Empty", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 701},
		{Name: "Fault/Missing UIDNEXT", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 801, OmitUIDNext: true},
		{Name: "Fault/Missing UIDVALIDITY", Delimiter: "/", Attributes: []string{string(imap.MailboxAttrHasNoChildren)}, UIDValidity: 901, OmitUIDValidity: true},
	}
}

// ExpectedFolders returns the folders that the backend should list for the
// default mailboxes, in the backend's order.
func ExpectedFolders() []ExpectedFolder {
	seeds := SeedFolders()
	type expected struct {
		folder   ExpectedFolder
		identity string
	}
	entries := make([]expected, 0, len(seeds))
	for _, seed := range seeds {
		path := seed.Name
		if seed.Delimiter != "" {
			path = strings.ReplaceAll(path, seed.Delimiter, "/")
		}
		fetchable := !hasAttr(seed.Attributes, string(imap.MailboxAttrNoSelect))
		folder := ExpectedFolder{
			Path:      path,
			Fetchable: fetchable,
		}
		if fetchable {
			folder.ScopeKey = expectedScopeKey(seed.Name, seed.Delimiter, seed.Delimiter != "")
		}
		entries = append(entries, expected{
			folder:   folder,
			identity: canonical(seed.Name),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].folder.Path != entries[j].folder.Path {
			return entries[i].folder.Path < entries[j].folder.Path
		}
		if entries[i].folder.ScopeKey != entries[j].folder.ScopeKey {
			return entries[i].folder.ScopeKey < entries[j].folder.ScopeKey
		}
		return entries[i].identity < entries[j].identity
	})

	out := make([]ExpectedFolder, len(entries))
	for i := range entries {
		out[i] = entries[i].folder
	}
	return out
}

func expectedScopeKey(identity, delimiter string, present bool) string {
	identity = canonical(identity)
	var buf bytes.Buffer
	buf.WriteString(scopeKeyMagic)
	writeUvarint(&buf, uint64(len(identity)))
	buf.WriteString(identity)
	if present {
		buf.WriteByte(1)
		writeUvarint(&buf, uint64(len(delimiter)))
		buf.WriteString(delimiter)
	} else {
		buf.WriteByte(0)
	}
	return scopeKeyPrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

func writeUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}
