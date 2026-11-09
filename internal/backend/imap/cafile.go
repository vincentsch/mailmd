package imap

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/vincentsch/mailmd/internal/backend"
)

// CAPool returns the certificates to trust: base, or the system's
// certificates when base is nil, and the certificates in the PEM file at
// path. This way a server whose certificate comes from a company's own
// certificate authority works without a change to the system.
func CAPool(path string, base *x509.CertPool) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		reason := "it can't be read"
		switch {
		case errors.Is(err, fs.ErrNotExist):
			reason = "the file does not exist"
		case errors.Is(err, fs.ErrPermission):
			reason = "you don't have permission to read it"
		}
		return nil, &backend.Problem{
			Text: fmt.Sprintf("Cannot read the CA file %s, because %s.", path, reason),
			Hint: "Check the path. To use another file, run the connect command again with --ca-file.",
			Err:  ErrTLS,
		}
	}
	var pool *x509.CertPool
	switch {
	case base != nil:
		pool = base.Clone()
	default:
		if pool, err = x509.SystemCertPool(); err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, &backend.Problem{
			Text: fmt.Sprintf("The CA file %s has no certificate in PEM format.", path),
			Hint: "A PEM file starts with -----BEGIN CERTIFICATE-----. You can convert a binary .cer or .der file with: openssl x509 -inform der -in ca.cer -out ca.pem",
			Err:  ErrTLS,
		}
	}
	return pool, nil
}
