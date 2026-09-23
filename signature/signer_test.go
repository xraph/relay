package signature_test

import (
	"errors"
	"testing"

	"github.com/xraph/relay/signature"
)

// mustSign fails the test rather than returning an error, for the cases that
// are not about the error path.
func mustSign(t *testing.T, s *signature.Signer, payload []byte, secret string, ts int64) string {
	t.Helper()
	sig, err := s.Sign(payload, secret, ts)
	if err != nil {
		t.Fatalf("Sign(%q): %v", secret, err)
	}
	return sig
}

func TestSignKnownVector(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"event":"test"}`)
	secret := "whsec_testsecret123"
	timestamp := int64(1700000000)

	got := mustSign(t, signer, payload, secret, timestamp)

	// A literal, not a recomputation. Rebuilding the expectation with the
	// same formula the implementation uses makes the two move together, so a
	// change to the signed content format passes here and silently breaks
	// every deployed receiver. This value is the contract.
	const expected = "v1=54cc009a0beb0cf5740946b4b78d7b071dcb80552fb1e211546c391896780fe5"

	if got != expected {
		t.Errorf("Sign() = %q, want %q. If you changed the signed content "+
			"format, every deployed receiver stops verifying.", got, expected)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"invoice_id":"inv_01h2x","amount":9900}`)
	secret := "whsec_roundtripsecret"
	timestamp := int64(1700000001)

	sig := mustSign(t, signer, payload, secret, timestamp)
	if !signer.Verify(payload, secret, timestamp, sig) {
		t.Error("Verify() returned false for valid signature")
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"original":true}`)
	secret := "whsec_tampersecret"
	timestamp := int64(1700000002)

	sig := mustSign(t, signer, payload, secret, timestamp)

	tampered := []byte(`{"original":false}`)
	if signer.Verify(tampered, secret, timestamp, sig) {
		t.Error("Verify() returned true for tampered payload")
	}
}

func TestVerifyWrongSecret(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"data":"value"}`)
	secret := "whsec_correct"
	timestamp := int64(1700000003)

	sig := mustSign(t, signer, payload, secret, timestamp)

	if signer.Verify(payload, "whsec_wrong", timestamp, sig) {
		t.Error("Verify() returned true for wrong secret")
	}
}

func TestVerifyWrongTimestamp(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"data":"value"}`)
	secret := "whsec_timestampsecret"
	timestamp := int64(1700000004)

	sig := mustSign(t, signer, payload, secret, timestamp)

	if signer.Verify(payload, secret, timestamp+1, sig) {
		t.Error("Verify() returned true for wrong timestamp")
	}
}

func TestSignatureFormat(t *testing.T) {
	signer := signature.NewSigner()
	sig := mustSign(t, signer, []byte("test"), "secret", 123)

	if len(sig) < 3 || sig[:3] != "v1=" {
		t.Errorf("signature should start with 'v1=', got %q", sig)
	}

	// v1= prefix (3) + 64 hex chars (SHA256 = 32 bytes = 64 hex)
	if len(sig) != 67 {
		t.Errorf("expected signature length 67, got %d", len(sig))
	}
}

// The bug this package is being changed to prevent.
//
// HMAC will produce a digest from any key including an empty one, so
// Sign("") returned a well-formed "v1=" plus 64 hex characters that Verify
// then accepted. On the wire it was indistinguishable from a real signature,
// and anybody who knew the payload and timestamp could reproduce it, so a
// receiver doing verification correctly still accepted forged deliveries.

func TestSignRefusesAnEmptySecret(t *testing.T) {
	_, err := signature.Sign([]byte(`{"a":1}`), "", 1750000000)
	if !errors.Is(err, signature.ErrNoSecret) {
		t.Fatalf("error = %v, want ErrNoSecret", err)
	}
}

func TestSignRefusesAWhitespaceSecret(t *testing.T) {
	// A secret of spaces is an absent secret wearing a disguise. Refusing
	// only "" leaves the check bypassable by a stray config value.
	_, err := signature.Sign([]byte(`{"a":1}`), "   ", 1750000000)
	if !errors.Is(err, signature.ErrNoSecret) {
		t.Fatalf("error = %v, want ErrNoSecret", err)
	}
}

func TestSignReturnsNoSignatureWhenItRefuses(t *testing.T) {
	// A caller that ignores the error must not find a usable signature in
	// the first return value.
	sig, _ := signature.Sign([]byte(`{"a":1}`), "", 1750000000)
	if sig != "" {
		t.Fatalf("Sign returned %q alongside its error, want the empty string", sig)
	}
}

func TestVerifyIsFalseForAnEmptySecret(t *testing.T) {
	// This exact string is what Sign produced for an empty secret before the
	// change, measured by running it. Verify returned true for it.
	payload := []byte(`{"a":1}`)
	ts := int64(1750000000)
	// HMAC-SHA256 of "1750000000.{\"a\":1}" keyed with the empty string.
	// This is what the old Sign returned for this exact payload, and what the
	// old Verify accepted. Computed independently, not by calling Sign: a
	// constant derived from the code under test cannot catch the code under
	// test regressing.
	forged := "v1=dbd4a33475f68e88faa5839a3b70ca63d45b3860d06fe440190cc57d8bd1c587"
	if signature.Verify(payload, "", ts, forged) {
		t.Fatal("Verify returned true for an empty secret")
	}
}

func TestVerifyIsFalseForAWhitespaceSecret(t *testing.T) {
	// Keyed with two spaces, over the same content. "v1=anything" would have
	// been rejected by any implementation, so it tested nothing.
	forged := "v1=a1e652ae20c70fa86686866f7ae91324bb6b517a8f6da48db9639c421ed39e39"
	if signature.Verify([]byte(`{"a":1}`), "  ", 1750000000, forged) {
		t.Fatal("Verify returned true for a whitespace secret")
	}
}

func TestSignStillRoundTripsWithARealSecret(t *testing.T) {
	payload := []byte(`{"a":1}`)
	sig, err := signature.Sign(payload, "whsec_test", 1750000000)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !signature.Verify(payload, "whsec_test", 1750000000, sig) {
		t.Fatal("a real secret no longer round-trips")
	}
}
