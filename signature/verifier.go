package signature

import "crypto/hmac"

// Verify checks whether the given signature matches the expected HMAC-SHA256
// signature for the payload, secret, and timestamp.
func (s *Signer) Verify(payload []byte, secret string, timestamp int64, sig string) bool {
	return Verify(payload, secret, timestamp, sig)
}

// Verify checks whether the given signature matches the expected HMAC-SHA256
// signature for the payload, secret, and timestamp.
func Verify(payload []byte, secret string, timestamp int64, sig string) bool {
	expected := Sign(payload, secret, timestamp)
	return hmac.Equal([]byte(expected), []byte(sig))
}
