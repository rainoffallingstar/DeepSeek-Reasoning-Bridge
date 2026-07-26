package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"
)

func synthesizeGPTSignature() string {
	payload := make([]byte, 73)
	payload[0] = 0x80
	binary.BigEndian.PutUint64(payload[1:9], uint64(time.Now().Unix()))
	_, _ = rand.Read(payload[9:])
	return base64.RawURLEncoding.EncodeToString(payload)
}

// isValidGPTSignature mirrors the transport-shape checks used by CPA. It does
// not attempt to decrypt the provider payload.
func isValidGPTSignature(signature string) bool {
	signature = strings.TrimSpace(signature)
	if signature == "" || !strings.HasPrefix(signature, "gAAAA") {
		return false
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(signature)
	if errDecode != nil {
		decoded, errDecode = base64.URLEncoding.DecodeString(signature)
	}
	if errDecode != nil || len(decoded) < 73 || decoded[0] != 0x80 {
		return false
	}
	ciphertextLength := len(decoded) - 1 - 8 - 16 - 32
	return ciphertextLength > 0 && ciphertextLength%16 == 0
}
