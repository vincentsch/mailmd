package mail

import "time"

// Address is an email address with an optional display name.
type Address struct {
	Name  string
	Email string
}

// Flags are the read, starred, answered and draft marks of a message. They
// are not part of the local_id, so a changed flag never renames a file.
type Flags struct {
	Seen     bool
	Flagged  bool
	Answered bool
	Draft    bool
}

// Attachment is one attachment of a message.
type Attachment struct {
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
	Inline    bool
	// Data is the content. It is nil when the content was not downloaded.
	Data []byte
}

// Message is one message as a backend delivers it to the writer.
type Message struct {
	// LocalID is mailmd's ID for the message, from DeriveLocalID.
	LocalID string
	// Ref is the provider's ID of the message, like an IMAP UID or a Gmail
	// message ID. It is not part of the local_id.
	Ref string
	// MessageID can be empty, and two messages can share one.
	MessageID  string
	InReplyTo  string
	References []string
	// ThreadID is the provider's conversation ID. It is not part of the
	// local_id, because providers can change it.
	ThreadID string

	From Address
	To   []Address
	Cc   []Address
	// Bcc is empty unless the backend can see it.
	Bcc     []Address
	ReplyTo []Address

	Subject string
	// Date is the Date header. Zero means it was missing or unreadable.
	Date time.Time
	// Received is when the provider received the message, such as the IMAP
	// INTERNALDATE. The writer uses it when Date is zero. It is not part of
	// the identity.
	Received time.Time
	// OmittedSize is set when the message was larger than the size limit.
	// The message then holds only its headers, and OmittedSize is its full
	// size in bytes.
	OmittedSize int64
	// ParseError is set when the message could not be read. It says why,
	// in a fixed phrase. The message then holds the headers that could be
	// read, and Raw holds the original bytes.
	ParseError string

	// Folders holds the provider's folder of the message, with "/" between
	// levels. NormalizeFolderPath makes the folder names on disk from it.
	Folders []string
	// Labels lists all Gmail labels of the message, for the frontmatter.
	Labels []string
	Flags  Flags

	// BodyText is the text part. Backends never put converted HTML here,
	// because the writer picks the body and converts HTML itself.
	BodyText string
	// BodyHTML is the HTML part as the message has it.
	BodyHTML string

	Attachments []Attachment
	// RawHeaders holds the headers, with keys in the form of
	// textproto.CanonicalMIMEHeaderKey. They are not part of the local_id.
	RawHeaders map[string][]string
	// Forwarded holds messages attached to this one as message/rfc822 parts,
	// so their text can be shown and searched. They are not part of the
	// local_id.
	Forwarded []Message
	// Raw is the original message, for --keep-raw. It is not part of the
	// local_id.
	Raw []byte
}
