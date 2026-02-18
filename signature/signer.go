// Package signature provides HMAC-SHA256 webhook signing and verification.
package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Signer computes HMAC-SHA256 signatures for webhook payloads.
type Signer struct{}

// NewSigner returns a new Signer.
func NewSigner() *Signer {
	return &Signer{}
}

// Sign generates the HMAC-SHA256 signature for the given payload.
// The content to sign is "{timestamp}.{payload}".
// Returns a versioned signature in the format "v1=<hex>".
func (s *Signer) Sign(payload []byte, secret string, timestamp int64) string {
	return Sign(payload, secret, timestamp)
}

// Sign generates the HMAC-SHA256 signature for the given payload.
// The content to sign is "{timestamp}.{payload}".
// Returns a versioned signature in the format "v1=<hex>".
func Sign(payload []byte, secret string, timestamp int64) string {
	content := fmt.Sprintf("%d.%s", timestamp, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(content))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}
