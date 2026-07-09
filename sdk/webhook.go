package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// VerifyHMACSHA256 checks if the hex signature matches the HMAC-SHA256 signature of the payload.
func VerifyHMACSHA256(payload []byte, signature string, secret []byte) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)
	actualMAC, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(actualMAC, expectedMAC)
}

// VerifyHMACSHA256HexSignature validates a signature that might contain a prefix (e.g. "sha256=").
func VerifyHMACSHA256HexSignature(payload []byte, signatureHeader string, prefix string, secret []byte) bool {
	sig := signatureHeader
	if len(prefix) > 0 && len(sig) > len(prefix) && sig[:len(prefix)] == prefix {
		sig = sig[len(prefix):]
	}
	return VerifyHMACSHA256(payload, sig, secret)
}
