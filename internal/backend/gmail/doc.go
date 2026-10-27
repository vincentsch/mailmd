// Package gmail reads a Gmail account through the Gmail API.
//
// Each message is fetched once, in raw form, and goes into one folder that is
// picked from its labels. The file lists all labels. The cursor holds Gmail's
// history ID, so the next sync asks only for what changed since then.
//
// The caller passes in an HTTP client that is already signed in. This package
// never loads logins or writes files.
package gmail
