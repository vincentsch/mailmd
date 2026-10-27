// Package mailfixtures has sample messages that several tests share.
//
// They are mail.Message values like a backend returns them. LocalID is empty,
// so the tests run the real code that computes it. Messages returns new values
// on each call. The package has only input data and no expected writer
// output.
package mailfixtures
