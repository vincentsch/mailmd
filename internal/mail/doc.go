// Package mail defines the message that every backend produces and the
// writer turns into a file.
//
// It has only data types and helpers that give the same result for the same
// input. It doesn't touch files, the network or the clock, and it imports no
// other mailmd package. Its helpers decide a message's local_id and the safe
// names of files and folders, so changing them renames mail on disk.
package mail
