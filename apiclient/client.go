// Package apiclient calls the portable JSON Lines TCP API.
package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/linkdata/autopaws/apiwire"
)

// Client opens a fresh connection for each call. Its address is immutable, so
// concurrent calls are independent and safe.
type Client struct {
	address string
}

// New returns a client for a TCP address such as "localhost:9329". Connections
// are established by Call, whose context owns their complete lifetime.
func New(address string) *Client { return &Client{address: address} }

// RemoteError is a stable error code returned by the server, with optional
// diagnostics. Use errors.Is with an apiwire.Code to test its identity.
type RemoteError struct {
	Code   apiwire.Code
	Detail string
}

func (err *RemoteError) Error() string {
	if err.Detail == "" {
		return err.Code.Error()
	}
	return err.Code.Error() + ": " + err.Detail
}

// Unwrap exposes the stable wire code without discarding its diagnostics.
func (err *RemoteError) Unwrap() error { return err.Code }

// Call sends one request, reads one response and closes its connection. Nil
// params become an empty object; nil result discards a validated success result.
// There is no default timeout: callers supply a deadline or cancel their context.
func (client *Client) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if params == nil {
		params = struct{}{}
	}
	request, err := json.Marshal(struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: 1, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("encode API request: %w", err)
	}
	request = append(request, '\n')
	if len(request) > apiwire.MaxRequestLine {
		return apiwire.ErrRequestTooLarge
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", client.address)
	if err != nil {
		return networkError(ctx, "dial API", err)
	}
	return exchange(ctx, conn, request, result)
}

func exchange(ctx context.Context, conn net.Conn, request []byte, result any) error {
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(canceled)
	})
	defer func() {
		if !stop() {
			<-canceled
		}
		_ = conn.Close()
	}()
	if _, err := conn.Write(request); err != nil {
		return networkError(ctx, "write API request", err)
	}
	line, err := apiwire.ReadLine(bufio.NewReader(conn), apiwire.MaxResponseLine)
	if err != nil {
		return networkError(ctx, "read API response", fmt.Errorf("%w: %w", apiwire.ErrProtocol, err))
	}
	return decodeResponse(line, result)
}

// Cancellation closes the connection to interrupt both reads and writes. Return
// the context identity rather than the resulting closed-network error.
func networkError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// Raw optional fields distinguish absence from null or an empty error string.
// In particular, a missing/null ok must never be mistaken for a remote failure.
type response struct {
	ID     json.RawMessage `json:"id"`
	OK     *bool           `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Detail json.RawMessage `json:"detail"`
}

func decodeResponse(line []byte, result any) error {
	var reply response
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return fmt.Errorf("%w: decode response: %w", apiwire.ErrProtocol, err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("%w: extra response data", apiwire.ErrProtocol)
	}
	if !bytes.Equal(reply.ID, []byte("1")) || reply.OK == nil {
		return fmt.Errorf("%w: missing ok or mismatched response id", apiwire.ErrProtocol)
	}
	if *reply.OK {
		if len(reply.Result) == 0 || len(reply.Error) != 0 || len(reply.Detail) != 0 {
			return fmt.Errorf("%w: incoherent success response", apiwire.ErrProtocol)
		}
		if result != nil {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				return fmt.Errorf("%w: decode result: %w", apiwire.ErrProtocol, err)
			}
		}
		return nil
	}
	var code *apiwire.Code
	if err := json.Unmarshal(reply.Error, &code); err != nil || code == nil || *code == "" || len(reply.Result) != 0 {
		return fmt.Errorf("%w: incoherent error response", apiwire.ErrProtocol)
	}
	remote := &RemoteError{Code: *code}
	if len(reply.Detail) != 0 {
		var detail *string
		if err := json.Unmarshal(reply.Detail, &detail); err != nil || detail == nil {
			return fmt.Errorf("%w: invalid error detail", apiwire.ErrProtocol)
		}
		remote.Detail = *detail
	}
	return remote
}
