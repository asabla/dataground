package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func New(prefix string) string {
	random := make([]byte, 10)
	if _, err := rand.Read(random); err != nil {
		panic("cryptographic identifier generation failed")
	}
	return prefix + "_" + hex.EncodeToString(random)
}

func Derived(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + "_" + hex.EncodeToString(digest[:10])
}
