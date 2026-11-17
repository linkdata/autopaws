// Package apiwire defines the portable JSON Lines API envelopes and framing.
package apiwire

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

// Line limits include the terminating newline.
const (
	MaxRequestLine  = 1 << 20
	MaxResponseLine = 64 << 20
)

// Code is a stable wire error identity, usable directly with errors.Is.
type Code string

func (code Code) Error() string { return string(code) }

const (
	ErrUnauthorized    Code = "unauthorized"
	ErrInvalidRequest  Code = "invalid request"
	ErrRequestTooLarge Code = "request too large"
	ErrUnknownMethod   Code = "unknown method"
	ErrInvalidParams   Code = "invalid params"
	ErrUnsupported     Code = "unsupported character"
	ErrCanceled        Code = "canceled"
	ErrTimeout         Code = "timeout"
	ErrUnavailable     Code = "unavailable"
	ErrBusy            Code = "device busy"
	ErrCaptureChanged  Code = "capture changed"
	ErrProtocol        Code = "protocol error"
)

// Request is one request on a fresh TCP connection. ID preserves JSON precision.
type Request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Token  string          `json:"token,omitempty"`
	Params json.RawMessage `json:"params"`
}

// Response carries either a result or a stable error with optional diagnostics.
type Response struct {
	ID     json.RawMessage `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  Code            `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
}

// ErrLineTooLarge means a newline-terminated line cannot fit in its limit.
var ErrLineTooLarge = errors.New("API line too large")

// ReadLine reads one line, including LF, without accumulating beyond limit.
// Empty EOF is io.EOF; EOF after an unterminated fragment is io.ErrUnexpectedEOF.
// Errors return nil data. Bytes read ahead remain in reader for its next user.
func ReadLine(reader *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, ErrLineTooLarge
	}
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > limit-len(line) {
			return nil, ErrLineTooLarge
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) != 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
