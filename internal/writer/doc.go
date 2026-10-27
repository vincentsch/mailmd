// Package writer turns messages into the Markdown files of an archive.
//
// It decides the file names, writes the frontmatter and the body, and saves
// the attachments next to each message. A file is written to a temporary
// file first and then renamed, so a crash never leaves half a file. Writing
// the same message again gives the same file. The package knows nothing
// about providers, logins or the sync state.
package writer
