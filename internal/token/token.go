// Package token validates optional authentication secrets.
package token

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ErrInvalid indicates a nonempty value that is not a nonblank UTF-8 secret.
var ErrInvalid = errors.New("expected a nonempty UTF-8 token")

// ErrConflict indicates that both direct and file-based secrets were selected.
var ErrConflict = errors.New("token and token-file cannot both be set")

// Resolve selects one optional secret source, rejecting conflicting settings.
func Resolve(value, path string) (secret string, err error) {
	if value != "" && path != "" {
		err = ErrConflict
	} else if value != "" {
		secret = strings.TrimSpace(value)
		if secret == "" || !utf8.ValidString(secret) {
			secret, err = "", ErrInvalid
		}
	} else {
		secret, err = ReadFile(path)
	}
	return
}

// ReadFile reads a configured UTF-8 secret once and trims surrounding whitespace.
// An empty path disables authentication; an explicitly selected empty file fails.
// Returned errors never include file contents.
func ReadFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" || !utf8.ValidString(secret) {
		return "", fmt.Errorf("token file %q: expected a nonempty UTF-8 secret", path)
	}
	return secret, nil
}

// Match accepts all requests when authentication is disabled by an empty secret.
func Match(secret, supplied string) bool {
	return secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(supplied)) == 1
}
