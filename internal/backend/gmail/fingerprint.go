package gmail

import (
	"crypto/sha256"
	"errors"
	"strings"
)

var errInvalidProfile = errors.New("gmail: invalid profile")

// accountFingerprint ties a cursor to the signed-in Gmail account. It is a
// hash, so the email address never shows up in the cursor or in output.
func accountFingerprint(profile gmailProfile) ([32]byte, error) {
	if profile.EmailAddress == "" {
		return [32]byte{}, errInvalidProfile
	}
	return sha256.Sum256([]byte("mailmd-gmail-account-v1\x00" + strings.ToLower(profile.EmailAddress))), nil
}
