package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNoSecret is returned when signing is attempted without a secret.
//
// An empty key is not a weak key, it is an absent one. HMAC will produce a
// well-formed digest from it, and that digest verifies against the same empty
// key, so the result is indistinguishable on the wire from a real signature
// and anybody who knows the payload and timestamp can reproduce it. A
// receiver doing verification correctly still accepts it.
//
// The refusal lives here rather than in each caller on purpose. A primitive
// that accepts a null key makes every caller responsible for a check they
// will eventually forget, and one forgotten check produces something that
// looks exactly like security and is not.
var ErrNoSecret = errors.New("relay/signature: signing secret is empty")

// Signer computes HMAC-SHA256 signatures for webhook payloads.
type Signer struct{}

// NewSigner returns a new Signer.
func NewSigner() *Signer {
	return &Signer{}
}

// Sign generates the HMAC-SHA256 signature for the given payload.
// The content to sign is "{timestamp}.{payload}".
// Returns a versioned signature in the format "v1=<hex>".
//
// Returns ErrNoSecret when secret is empty or only whitespace.
func (s *Signer) Sign(payload []byte, secret string, timestamp int64) (string, error) {
	return Sign(payload, secret, timestamp)
}

// Sign generates the HMAC-SHA256 signature for the given payload.
// The content to sign is "{timestamp}.{payload}".
// Returns a versioned signature in the format "v1=<hex>".
//
// Returns ErrNoSecret when secret is empty or only whitespace.
func Sign(payload []byte, secret string, timestamp int64) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrNoSecret
	}
	content := fmt.Sprintf("%d.%s", timestamp, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(content))
	return "v1=" + hex.EncodeToString(mac.Sum(nil)), nil
}
