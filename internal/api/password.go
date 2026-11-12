package api

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Password hashes are PBKDF2-HMAC-SHA256 (standard library only):
// "pbkdf2-sha256$<iterations>$<salt b64>$<key b64>".
const (
	hashScheme = "pbkdf2-sha256"
	hashSalt   = 16
	hashKeyLen = 32
)

// hashIter is the PBKDF2 iteration count (OWASP 2023: 600 000 for SHA-256). Tests lower it.
var hashIter = 600_000

func hashPassword(pw string) (string, error) {
	salt := make([]byte, hashSalt)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, hashIter, hashKeyLen)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, hashIter, b64(salt), b64(key)), nil
}

// checkPassword reports whether pw matches a hash made by hashPassword.
func checkPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 10_000_000 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is compared against when the user does not exist, so the answer takes as long
// as for a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, _ := hashPassword("pk95-dummy-password")
	return h
})
