// Package rfc822 parses raw messages for the backends.
//
// It reads the headers, the bodies and the attachments of a message, and it
// computes the message's local ID. Folders and flags come from the provider,
// so the backends add them.
package rfc822
