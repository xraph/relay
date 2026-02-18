package signature_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/xraph/relay/signature"
)

func TestSignKnownVector(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"event":"test"}`)
	secret := "whsec_testsecret123"
	timestamp := int64(1700000000)

	got := signer.Sign(payload, secret, timestamp)

	// Compute expected HMAC-SHA256 independently.
	content := fmt.Sprintf("%d.%s", timestamp, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(content))
	expected := "v1=" + hex.EncodeToString(mac.Sum(nil))

	if got != expected {
		t.Errorf("Sign() = %q, want %q", got, expected)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"invoice_id":"inv_01h2x","amount":9900}`)
	secret := "whsec_roundtripsecret"
	timestamp := int64(1700000001)

	sig := signer.Sign(payload, secret, timestamp)
	if !signer.Verify(payload, secret, timestamp, sig) {
		t.Error("Verify() returned false for valid signature")
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"original":true}`)
	secret := "whsec_tampersecret"
	timestamp := int64(1700000002)

	sig := signer.Sign(payload, secret, timestamp)

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

	sig := signer.Sign(payload, secret, timestamp)

	if signer.Verify(payload, "whsec_wrong", timestamp, sig) {
		t.Error("Verify() returned true for wrong secret")
	}
}

func TestVerifyWrongTimestamp(t *testing.T) {
	signer := signature.NewSigner()
	payload := []byte(`{"data":"value"}`)
	secret := "whsec_timestampsecret"
	timestamp := int64(1700000004)

	sig := signer.Sign(payload, secret, timestamp)

	if signer.Verify(payload, secret, timestamp+1, sig) {
		t.Error("Verify() returned true for wrong timestamp")
	}
}

func TestSignatureFormat(t *testing.T) {
	signer := signature.NewSigner()
	sig := signer.Sign([]byte("test"), "secret", 123)

	if len(sig) < 3 || sig[:3] != "v1=" {
		t.Errorf("signature should start with 'v1=', got %q", sig)
	}

	// v1= prefix (3) + 64 hex chars (SHA256 = 32 bytes = 64 hex)
	if len(sig) != 67 {
		t.Errorf("expected signature length 67, got %d", len(sig))
	}
}
