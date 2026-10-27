// Package auth signs in to Google and Microsoft and saves the logins in the
// config folder.
//
// It prints nothing and doesn't write into the archive. Callers pass a folder
// name that is already safe, and they get the secret values back, so the
// command layer can hide them in its output.
package auth
