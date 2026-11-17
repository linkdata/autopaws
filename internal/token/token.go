// Package token loads the optional shared secret used by the daemon and MCP.
package token

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

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
