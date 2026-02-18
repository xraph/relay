package signature_test

import (
	"strings"
	"testing"

	"github.com/xraph/relay/signature"
)

func TestGenerateSecretFormat(t *testing.T) {
	secret := signature.GenerateSecret()

	if !strings.HasPrefix(secret, "whsec_") {
		t.Errorf("expected prefix 'whsec_', got %q", secret)
	}

	// whsec_ (6) + 64 hex chars (32 bytes) = 70 total
	if len(secret) != 70 {
		t.Errorf("expected length 70, got %d for %q", len(secret), secret)
	}
}

func TestGenerateSecretUniqueness(t *testing.T) {
	a := signature.GenerateSecret()
	b := signature.GenerateSecret()
	if a == b {
		t.Errorf("two consecutive GenerateSecret() calls returned the same value: %q", a)
	}
}

func TestGenerateSecretHexChars(t *testing.T) {
	secret := signature.GenerateSecret()
	hex := strings.TrimPrefix(secret, "whsec_")

	for i, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("non-hex character at position %d: %c in %q", i, c, hex)
		}
	}
}
