package gmail

import (
	"encoding/base64"
	"errors"
	"time"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/rfc822"
	mdmail "github.com/vincentsch/mailmd/internal/mail"
)

// normalizeGmailMessage decodes the raw message and parses it with the rfc822
// package. Then it adds what only Gmail knows, like the labels and the thread
// ID.
//
// A message that can't be read is still returned, with the headers that
// could be read and its original bytes, together with the error.
func normalizeGmailMessage(msg gmailMessage, catalog labelCatalog) (mdmail.Message, error) {
	raw, err := decodeRawMessage(msg.Raw)
	if err != nil {
		raw = []byte(msg.Raw)
	}
	out, err := rfc822.Normalize(raw)
	if err != nil {
		out = rfc822.Unreadable(raw, err)
	}
	out.ThreadID = msg.ThreadID
	out.Raw = raw
	var folder string
	folder, out.Labels, out.Flags = catalog.labelsAndFlags(msg.LabelIDs)
	out.Folders = []string{folder}
	out.Ref = msg.ID
	if msg.InternalDate > 0 {
		out.Received = time.UnixMilli(msg.InternalDate)
	}
	return out, err
}

// decodeRawMessage reads Gmail's base64url text, with or without padding.
func decodeRawMessage(raw string) ([]byte, error) {
	if raw == "" {
		return nil, rfc822.ErrParse
	}
	if data, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return data, nil
	}
	data, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return nil, rfc822.ErrParse
	}
	return data, nil
}

func diagnosticForMessage(scopeKey, backendName, ref string, reason mdbackend.DiagnosticReason) mdbackend.Diagnostic {
	return mdbackend.Diagnostic{
		ScopeKey: scopeKey,
		Ref:      ref,
		Reason:   reason,
		Backend:  backendName,
	}
}

func diagnosticReasonForNormalizeError(err error) mdbackend.DiagnosticReason {
	if errors.Is(err, rfc822.ErrIdentity) {
		return mdbackend.ReasonIdentityFailure
	}
	return mdbackend.ReasonParseFailure
}
