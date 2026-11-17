package apiwire_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
)

func TestReadLine(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		limit int
		want  string
		err   error
	}{
		{"line", "{}\n", 3, "{}\n", nil},
		{"CRLF", "{}\r\n", 4, "{}\r\n", nil},
		{"fragments", strings.Repeat("x", 99) + "\n", 100, strings.Repeat("x", 99) + "\n", nil},
		{"too long", strings.Repeat("x", 100) + "\n", 100, "", apiwire.ErrLineTooLarge},
		{"empty EOF", "", 100, "", io.EOF},
		{"partial EOF", "{}", 3, "", io.ErrUnexpectedEOF},
		{"zero limit", "\n", 0, "", apiwire.ErrLineTooLarge},
		{"negative limit", "\n", -1, "", apiwire.ErrLineTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := apiwire.ReadLine(bufio.NewReaderSize(strings.NewReader(test.input), 16), test.limit)
			if !errors.Is(err, test.err) || string(got) != test.want || err != nil && got != nil {
				t.Fatalf("ReadLine=%q, %v; want %q, %v", got, err, test.want, test.err)
			}
		})
	}
	reader := bufio.NewReader(strings.NewReader("first\nsecond\n"))
	for _, want := range []string{"first\n", "second\n"} {
		if got, err := apiwire.ReadLine(reader, 10); err != nil || string(got) != want {
			t.Fatalf("read-ahead was lost: %q, %v", got, err)
		}
	}
	failure := errors.New("read failed")
	if got, err := apiwire.ReadLine(bufio.NewReader(io.MultiReader(strings.NewReader("fragment"), failingReader{failure})), 100); got != nil || !errors.Is(err, failure) {
		t.Fatalf("read error was not preserved: %q, %v", got, err)
	}
}

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestEnvelopePrecisionAndErrorIdentity(t *testing.T) {
	request := apiwire.Request{ID: json.RawMessage(`9007199254740993`), Method: "ping", Params: json.RawMessage(`{}`)}
	data, err := json.Marshal(request)
	if err != nil || !bytes.Contains(data, []byte(`"id":9007199254740993`)) {
		t.Fatalf("request ID lost precision: %s, %v", data, err)
	}
	response := apiwire.Response{ID: request.ID, OK: false, Error: apiwire.ErrUnavailable, Detail: "device absent"}
	data, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded apiwire.Response
	if err := json.Unmarshal(data, &decoded); err != nil || !bytes.Equal(decoded.ID, request.ID) || decoded.Error != apiwire.ErrUnavailable || decoded.Detail != "device absent" {
		t.Fatalf("response roundtrip=%+v, %v", decoded, err)
	}
	for _, code := range []apiwire.Code{
		apiwire.ErrInvalidRequest, apiwire.ErrRequestTooLarge, apiwire.ErrUnknownMethod,
		apiwire.ErrInvalidParams, apiwire.ErrUnsupported, apiwire.ErrCanceled,
		apiwire.ErrTimeout, apiwire.ErrUnavailable, apiwire.ErrBusy,
		apiwire.ErrCaptureChanged, apiwire.ErrProtocol,
	} {
		if !errors.Is(code, code) || code.Error() != string(code) {
			t.Fatalf("invalid error code identity: %q", code)
		}
	}
	if apiwire.MaxRequestLine != 1<<20 || apiwire.MaxResponseLine != 64<<20 {
		t.Fatal("wire line limits changed")
	}
}
