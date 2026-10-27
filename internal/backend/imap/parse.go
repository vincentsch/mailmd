package imap

import (
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/mail"
)

// normalizeFlags picks out the four system flags that mailmd writes. Other
// flags and keywords are ignored.
func normalizeFlags(flags []imap.Flag) mail.Flags {
	var out mail.Flags
	for _, flag := range flags {
		switch {
		case strings.EqualFold(string(flag), string(imap.FlagSeen)):
			out.Seen = true
		case strings.EqualFold(string(flag), string(imap.FlagFlagged)):
			out.Flagged = true
		case strings.EqualFold(string(flag), string(imap.FlagAnswered)):
			out.Answered = true
		case strings.EqualFold(string(flag), string(imap.FlagDraft)):
			out.Draft = true
		}
	}
	return out
}
