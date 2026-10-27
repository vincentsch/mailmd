package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	localIDVersion = "mailmd-localid-v1"
	localIDPrefix  = localIDVersion + "-"
)

// ErrInsufficientIdentity means a message has nothing that a local_id could
// be computed from.
var ErrInsufficientIdentity = errors.New("mail: insufficient identity to derive local id")

// normalizeMessageIDLike makes Message-ID style values from different
// providers compare equal. It doesn't parse them, because a broken header is
// still useful for the identity.
func normalizeMessageIDLike(s string) string {
	return strings.ToLower(collapseASCIISpace(stripOneAnglePair(trimASCIISpace(s))))
}

func normalizeEmail(s string) string {
	return strings.ToLower(trimASCIISpace(s))
}

// emailSet ignores the order of the recipients and their display names.
// Display names change more often than the addresses.
func emailSet(list []Address) string {
	values := make([]string, 0, len(list))
	for _, a := range list {
		if email := normalizeEmail(a.Email); email != "" {
			values = append(values, email)
		}
	}
	return strings.Join(sortedUnique(values), "\n")
}

// nfcCollapse normalizes free text for the identity, so the same text in
// another Unicode form or with other spacing gives the same ID.
func nfcCollapse(s string) string {
	return collapseASCIISpace(normalizeLineEndings(toNFC(s)))
}

func normalizeMIME(s string) string {
	return strings.ToLower(trimASCIISpace(s))
}

// normalizeContentID doesn't change the case, because HTML bodies refer to
// Content-ID values as they are. It removes the angle brackets, so <a> and a
// give the same ID.
func normalizeContentID(s string) string {
	return collapseASCIISpace(stripOneAnglePair(nfcCollapse(s)))
}

// referencesField works like emailSet. Empty entries are dropped, and the
// order doesn't change the ID.
func referencesField(refs []string) string {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		if normalized := normalizeMessageIDLike(ref); normalized != "" {
			values = append(values, normalized)
		}
	}
	return strings.Join(sortedUnique(values), "\n")
}

// attachmentsSignature uses the details of each attachment, like its name and
// size, but not its content. So a message gets the same ID whether or not its
// attachments were downloaded.
func attachmentsSignature(atts []Attachment) string {
	lines := make([]string, 0, len(atts))
	for _, a := range atts {
		filename := nfcCollapse(a.Filename)
		mimeType := normalizeMIME(a.MIMEType)
		contentID := normalizeContentID(a.ContentID)
		// An attachment without any details says nothing about the message.
		// It must not turn an empty message into one with an ID.
		if filename == "" && mimeType == "" && contentID == "" && a.Size == 0 && !a.Inline {
			continue
		}

		line := filename + "\t" +
			mimeType + "\t" +
			strconv.FormatInt(a.Size, 10) + "\t" +
			contentID + "\t" +
			"inline=" + strconv.FormatBool(a.Inline)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// bodyDigest hashes the body as the backend delivered it, the text part or
// else the raw HTML. It doesn't hash the converted Markdown, so a change in
// the conversion doesn't give old messages new IDs.
func bodyDigest(m Message) string {
	source := NormalizeBodyForRender(m.BodyText)
	if source == "" {
		source = NormalizeBodyForRender(m.BodyHTML)
	}
	if source == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// DeriveLocalID computes the local_id of a message from its headers and body.
// Things that change after delivery don't count, like the folder or the
// flags. Display names and attachment content also don't count.
// It returns ErrInsufficientIdentity when the message has nothing to compute
// the ID from.
func DeriveLocalID(m Message) (string, error) {
	date := ""
	if !m.Date.IsZero() {
		date = NormalizeDate(m.Date, time.UTC).Truncate(time.Second).Format(time.RFC3339)
	}

	// Changing the field order or the normalized values changes the file name
	// of every message, so both must stay as they are.
	fields := []struct {
		name  string
		value string
	}{
		{"version", localIDVersion},
		{"message_id", normalizeMessageIDLike(m.MessageID)},
		{"date", date},
		{"from_email", normalizeEmail(m.From.Email)},
		{"to_emails", emailSet(m.To)},
		{"cc_emails", emailSet(m.Cc)},
		{"bcc_emails", emailSet(m.Bcc)},
		{"reply_to_emails", emailSet(m.ReplyTo)},
		{"subject", nfcCollapse(m.Subject)},
		{"in_reply_to", normalizeMessageIDLike(m.InReplyTo)},
		{"references", referencesField(m.References)},
		{"attachments_signature", attachmentsSignature(m.Attachments)},
		{"body_sha256", bodyDigest(m)},
	}

	// The version is not message data. Without any other field, all empty
	// messages would share one ID.
	hasIdentity := false
	for _, field := range fields[1:] {
		if field.value != "" {
			hasIdentity = true
			break
		}
	}
	if !hasIdentity {
		return "", ErrInsufficientIdentity
	}

	// Each value goes in with its length, so a value that contains a separator
	// can't make two different messages hash the same.
	var buf bytes.Buffer
	for _, field := range fields {
		buf.WriteString(field.name)
		buf.WriteByte(0)
		buf.WriteString(strconv.Itoa(len(field.value)))
		buf.WriteByte(0)
		buf.WriteString(field.value)
		buf.WriteByte(0)
	}

	sum := sha256.Sum256(buf.Bytes())
	return localIDPrefix + hex.EncodeToString(sum[:]), nil
}

// RawLocalID derives a local id from the raw bytes of a message that could
// not be read. The same bytes always give the same id, and different bytes
// never share one.
func RawLocalID(raw []byte) string {
	sum := sha256.Sum256(append([]byte(localIDVersion+"\x00raw\x00"), raw...))
	return localIDPrefix + hex.EncodeToString(sum[:])
}

// EnsureLocalID returns m with LocalID set. A LocalID that is already set
// stays as it is.
func EnsureLocalID(m Message) (Message, error) {
	if m.LocalID != "" {
		return m, nil
	}
	id, err := DeriveLocalID(m)
	if err != nil {
		return m, err
	}
	m.LocalID = id
	return m, nil
}

// ShortID returns the 16 hex characters that go into a file name. For a
// normal local_id these are its first 16 hex characters. Any other ID is
// hashed first.
func ShortID(localID string) string {
	if strings.HasPrefix(localID, localIDPrefix) {
		rem := localID[len(localIDPrefix):]
		if isLowerHex16(rem) {
			return rem[:16]
		}
	}
	sum := sha256.Sum256([]byte(localID))
	return hex.EncodeToString(sum[:])[:16]
}
