package fsx

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// HashBytes computes the lowercase hexadecimal SHA-256 hash of a byte slice.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashString computes the lowercase hexadecimal SHA-256 hash of a string.
func HashString(s string) string {
	return HashBytes([]byte(s))
}

// HashReader computes the SHA-256 hash and total byte count of a reader stream.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
