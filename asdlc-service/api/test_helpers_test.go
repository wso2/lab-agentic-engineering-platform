package api

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// encodePKCS1 returns the PKCS#1-PEM encoding of priv. Test helper —
// matches the format setup-asdlc.sh writes via openssl genrsa.
func encodePKCS1(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(priv)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: der,
	})
}
