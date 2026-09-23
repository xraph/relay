package signature

import "crypto/hmac"

// Verify checks whether the given signature matches the expected HMAC-SHA256
// signature for the payload, secret, and timestamp.
//
// An empty secret is always false. There is no signature that can be correct
// under a key that does not exist, and returning true for one is how a forged
// delivery gets accepted by a receiver doing everything right.
func (s *Signer) Verify(payload []byte, secret string, timestamp int64, sig string) bool {
	return Verify(payload, secret, timestamp, sig)
}

// Verify checks whether the given signature matches the expected HMAC-SHA256
// signature for the payload, secret, and timestamp.
//
// An empty secret is always false. There is no signature that can be correct
// under a key that does not exist.
func Verify(payload []byte, secret string, timestamp int64, sig string) bool {
	expected, err := Sign(payload, secret, timestamp)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(sig))
}
