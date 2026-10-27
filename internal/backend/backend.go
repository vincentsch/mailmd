package backend

import (
	"context"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

// Cursor is a backend's saved position in a folder. The sync stores it as
// it is, and only the backend that wrote it reads it.
type Cursor []byte

// Backend is what the sync needs from a mail provider. Its methods only
// read.
//
// Folders and Fetch talk to the provider and stop when ctx ends. Close
// returns quickly, and calling it twice is fine. A Backend is used by one
// goroutine at a time.
type Backend interface {
	Name() string
	Folders(ctx context.Context) ([]Folder, error)
	Fetch(ctx context.Context, req FetchRequest) (FetchResult, error)
	Close() error
}

// Folder is one folder or label, as the provider shows it.
type Folder struct {
	Path      string
	Fetchable bool
	ScopeKey  string
	// Role is the folder's special use when the provider reports one, such
	// as RoleTrash. Sync uses it for the default folder exclusions.
	Role Role
}

// Role names the special use of a folder.
type Role string

const (
	RoleNone    Role = ""
	RoleInbox   Role = "inbox"
	RoleSent    Role = "sent"
	RoleDrafts  Role = "drafts"
	RoleArchive Role = "archive"
	RoleJunk    Role = "junk"
	RoleTrash   Role = "trash"
	// RoleAll is a virtual folder that shows every message again, like
	// Gmail's "All Mail".
	RoleAll Role = "all"
	// RoleFlagged and RoleImportant are virtual folders that repeat messages
	// stored in other folders, like Gmail's "Starred" and "Important".
	RoleFlagged   Role = "flagged"
	RoleImportant Role = "important"
)

// Addresser is implemented by backends that can ask the provider for the
// mailbox's email address. Connect saves it with the account.
type Addresser interface {
	Address(ctx context.Context) (string, error)
}

// Refresher is implemented by backends that can report the current flags
// of the messages they already delivered. A message missing from the report
// was deleted on the server.
type Refresher interface {
	Refresh(ctx context.Context, req RefreshRequest) (RefreshResult, error)
}

// Refetcher is implemented by backends that can fetch messages again by
// their Ref, like a message that was larger than the size limit before.
type Refetcher interface {
	// FetchRefs fetches messages of the request's scope by their Ref. The
	// request's cursor is not used. A message that is no longer there is
	// reported with ReasonVanished.
	FetchRefs(ctx context.Context, req FetchRequest, refs []string) (FetchResult, error)
}

// RefreshRequest names the folder and the cursor that Fetch returned last.
type RefreshRequest struct {
	ScopeKey string
	Cursor   Cursor
}

// RefreshResult holds the current flags of a folder's messages.
type RefreshResult struct {
	// Unchanged means nothing changed in the folder since the last refresh.
	// Flags is nil then.
	Unchanged bool
	// Flags holds the flags of every message the cursor covers, by the
	// message's Ref.
	Flags map[string]mail.Flags
	// Cursor replaces the folder's cursor. When it is nil, the old one stays.
	Cursor Cursor
}

// FetchRequest names the scope to fetch and the position where the last
// fetch stopped.
type FetchRequest struct {
	ScopeKey        string
	Cursor          Cursor
	Limit           int
	FolderPath      string
	MaxMessageBytes int64
	MaxBatchBytes   int64
	// Since limits fetching to messages received on or after this day. Zero
	// means all messages.
	Since time.Time
	// SkipJunk asks providers that list all mail at once, like the Gmail
	// API, to leave out spam and trash.
	SkipJunk bool
}

// FetchResult is a successful fetch. Diagnostics report messages that were
// left out or saved only in part. They are not errors.
type FetchResult struct {
	// RawBytes counts the raw size of the fetched messages as the provider
	// sent them, broken messages included.
	RawBytes    int64
	Messages    []mail.Message
	Cursor      Cursor
	Done        bool
	Diagnostics []Diagnostic
	// Remaining is how many messages the folder still has to fetch, when the
	// backend knows. Zero means none or unknown.
	Remaining int
}

// DiagnosticReason says why a message was left out or saved only in part.
// The reasons are the same for every provider.
type DiagnosticReason string

const (
	ReasonParseFailure       DiagnosticReason = "parse_failure"
	ReasonIdentityFailure    DiagnosticReason = "identity_failure"
	ReasonVanished           DiagnosticReason = "vanished"
	ReasonUnsupportedContent DiagnosticReason = "unsupported_content"
	ReasonRemoteRemoved      DiagnosticReason = "remote_removed"
	// ReasonTooLarge marks a message that was larger than the size limit.
	// Its headers are saved without the body.
	ReasonTooLarge DiagnosticReason = "too_large"
)

// Diagnostic reports one message that was left out or saved only in part.
// The backend has already moved its position past that message.
type Diagnostic struct {
	ScopeKey string
	Ref      string
	Reason   DiagnosticReason
	Backend  string
}
