// Package imap reads mail from an IMAP server.
//
// It opens each folder with EXAMINE and downloads messages with BODY.PEEK,
// so it never changes flags or messages on the server. A folder's cursor
// holds its UIDVALIDITY and the last UID mailmd has handled. When the server
// renumbers a folder, which shows as a new UIDVALIDITY, the folder starts
// over and files that already exist are left as they are.
package imap
