package signature

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateSecret creates a cryptographically random signing secret.
// Format: "whsec_" + 32 bytes hex = 70 characters total.
func GenerateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("relay: failed to generate random secret: " + err.Error())
	}
	return "whsec_" + hex.EncodeToString(b)
}
