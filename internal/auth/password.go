package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"golang.org/x/crypto/argon2"
)

func HashSecret(s string) string {
	b := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func Password(s string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	return base64.RawStdEncoding.EncodeToString(append(salt, key(s, salt)...))
}
func key(s string, salt []byte) []byte { return argon2.IDKey([]byte(s), salt, 3, 64*1024, 2, 32) }
func Verify(s, stored string) bool {
	b, e := base64.RawStdEncoding.DecodeString(stored)
	valid := e == nil && len(b) == 48
	if !valid {
		b = make([]byte, 48)
	}
	return subtle.ConstantTimeCompare(key(s, b[:16]), b[16:]) == 1 && valid
}
