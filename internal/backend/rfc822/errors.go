package rfc822

import (
	"errors"
	"fmt"
)

var (
	// ErrParse means a message can't be parsed. The error carries a fixed
	// reason and nothing from the message itself.
	ErrParse = errors.New("rfc822: parse failure")
	// ErrIdentity means a message could be parsed, but it has too little in
	// it to compute a local ID from.
	ErrIdentity = errors.New("rfc822: identity failure")
)

// Reasons why a message could not be read. They are fixed phrases, so they
// never carry message content, and Unreadable shows them in the file.
const (
	reasonHeader    = "the header section is broken"
	reasonStructure = "the MIME structure is broken"
	reasonEncoding  = "a part has a broken transfer encoding"
	reasonCutOff    = "the message ends in the middle of a part"
	reasonEmpty     = "the message has no headers and no text"
)

func parseFailure(reason string) error {
	return fmt.Errorf("%w: %s", ErrParse, reason)
}
