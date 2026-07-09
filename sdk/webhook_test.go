package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyWebhookSignature(t *testing.T) {
	secret := []byte("my_super_secret_webhook_key")
	payload := []byte(`{"event":"test","status":"ok"}`)

	sig := hmacSignHex(payload, secret)

	if !VerifyHMACSHA256(payload, sig, secret) {
		t.Errorf("HMAC signature verification failed for signature %s", sig)
	}

	// Test prefixed version (e.g. "sha256=abcdef...")
	sigWithPrefix := "sha256=" + sig
	if !VerifyHMACSHA256HexSignature(payload, sigWithPrefix, "sha256=", secret) {
		t.Errorf("Prefixed HMAC signature verification failed")
	}

	// Test incorrect secret
	if VerifyHMACSHA256(payload, sig, []byte("wrong_secret")) {
		t.Errorf("Expected signature verification to fail with incorrect secret, but it succeeded")
	}

	// Test corrupted payload
	corruptedPayload := []byte(`{"event":"test","status":"corrupted"}`)
	if VerifyHMACSHA256(corruptedPayload, sig, secret) {
		t.Errorf("Expected signature verification to fail with corrupted payload, but it succeeded")
	}
}

func hmacSignHex(payload []byte, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
