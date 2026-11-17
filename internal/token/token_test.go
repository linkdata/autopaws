package token

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile(t *testing.T) {
	if value, err := ReadFile(""); value != "" || err != nil {
		t.Fatalf("disabled token = %q, %v", value, err)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if _, err := ReadFile(missing); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("read cause/path lost: %v", err)
	}
	for _, contents := range []string{"", " \r\n\t", "bad\xffsecret", " \tå+; percent% secret\r\n"} {
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(path)
		if strings.Contains(contents, "å") {
			if err != nil || got != "å+; percent% secret" {
				t.Fatalf("trimmed UTF-8 token = %q, %v", got, err)
			}
		} else if err == nil || got != "" || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "bad") {
			t.Fatalf("invalid token accepted or leaked: %q, %v", got, err)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, vector := range []struct {
		secret, supplied string
		want             bool
	}{{"", "", true}, {"", "anything", true}, {"secret", "secret", true}, {"secret", "", false}, {"secret", "SECRET", false}, {"same", "size", false}, {"å+", "å+", true}} {
		if got := Match(vector.secret, vector.supplied); got != vector.want {
			t.Errorf("Match = %v; want %v", got, vector.want)
		}
	}
}
