//go:build windows && (amd64 || arm64)

package api

import (
	"fmt"
	"io/fs"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
	"github.com/linkdata/autopaws/hid"
	"golang.org/x/sys/windows"
)

func TestWindowsLayoutPathErrors(t *testing.T) {
	for _, test := range []struct {
		cause error
		code  apiwire.Code
	}{
		{windows.ERROR_INVALID_NAME, apiwire.ErrInvalidParams},
		{windows.ERROR_FILENAME_EXCED_RANGE, apiwire.ErrInvalidParams},
		{windows.ERROR_ACCESS_DENIED, apiwire.ErrUnavailable},
	} {
		err := fmt.Errorf("%w: %w", hid.ErrLayout, &fs.PathError{Op: "open", Path: "layout.yaml", Err: test.cause})
		if code, _ := classifyError(err); code != test.code {
			t.Fatalf("%v classified as %s; want %s", test.cause, code, test.code)
		}
	}
}
