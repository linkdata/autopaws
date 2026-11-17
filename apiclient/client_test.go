package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linkdata/autopaws/apiwire"
)

// Each fixture accepts one real TCP connection. Its deadline also bounds test
// failures where a client forgets to send, receive or close its connection.
func fixture(t *testing.T, handle func(*net.TCPConn)) string {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptTCP()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("accept fixture: %v", err)
			}
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Errorf("fixture deadline: %v", err)
			return
		}
		handle(conn)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return listener.Addr().String()
}

func readRequest(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	line, err := apiwire.ReadLine(bufio.NewReader(conn), apiwire.MaxRequestLine)
	if err != nil {
		t.Errorf("read fixture request: %v", err)
	}
	return line
}

func replyFixture(t *testing.T, reply string) string {
	t.Helper()
	return fixture(t, func(conn *net.TCPConn) {
		_ = readRequest(t, conn)
		if _, err := io.WriteString(conn, reply); err != nil {
			t.Errorf("write fixture reply: %v", err)
		}
	})
}

func callContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCall(t *testing.T) {
	closed := make(chan struct{})
	address := fixture(t, func(conn *net.TCPConn) {
		line := readRequest(t, conn)
		if string(line) != "{\"id\":1,\"method\":\"capture_frame\",\"params\":{}}\n" {
			t.Errorf("request = %s", line)
		}
		// A half-close is not a terminator: the client must leave its write side
		// open while waiting for the reply.
		if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			t.Error(err)
			return
		}
		var extra [1]byte
		_, err := conn.Read(extra[:])
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("read while client waits = %v, want timeout", err)
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		if _, err := io.WriteString(conn, "{\"id\":1,\"ok\":true,\"result\":{\"jpeg_b64\":\"AAEC/w==\",\"width\":12}}\r\n"); err != nil {
			t.Error(err)
			return
		}
		if _, err := conn.Read(extra[:]); !errors.Is(err, io.EOF) {
			t.Errorf("read after reply = %v, want EOF", err)
		}
		close(closed)
	})
	var result struct {
		JPEG  []byte `json:"jpeg_b64"`
		Width int    `json:"width"`
	}
	if err := New(address).Call(callContext(t), "capture_frame", nil, &result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.JPEG, []byte{0, 1, 2, 255}) || result.Width != 12 {
		t.Fatalf("result = %#v", result)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not close its completed connection")
	}
}

func TestCallDiscardAndNullResult(t *testing.T) {
	for _, result := range []string{`{"anything":[1,2,3]}`, `null`} {
		t.Run(result, func(t *testing.T) {
			address := replyFixture(t, `{"id":1,"ok":true,"result":`+result+"}\n")
			if err := New(address).Call(callContext(t), "ping", struct{}{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoteErrors(t *testing.T) {
	codes := []apiwire.Code{
		apiwire.ErrUnauthorized, apiwire.ErrInvalidRequest, apiwire.ErrRequestTooLarge, apiwire.ErrUnknownMethod,
		apiwire.ErrInvalidParams, apiwire.ErrUnsupported, apiwire.ErrCanceled,
		apiwire.ErrTimeout, apiwire.ErrUnavailable, apiwire.ErrBusy,
		apiwire.ErrCaptureChanged, apiwire.ErrProtocol, "future server error",
	}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			for _, detail := range []string{"", "invalid coordinate (2000, 20)"} {
				response := apiwire.Response{ID: json.RawMessage("1"), Error: code, Detail: detail}
				line, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				address := replyFixture(t, string(line)+"\n")
				err = New(address).Call(callContext(t), "example", nil, nil)
				var remote *RemoteError
				if !errors.Is(err, code) || !errors.As(err, &remote) || remote.Code != code || remote.Detail != detail {
					t.Fatalf("error = %#v, want code %q detail %q", err, code, detail)
				}
				want := string(code)
				if detail != "" {
					want += ": " + detail
				}
				if err.Error() != want {
					t.Fatalf("error string = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":        `{`,
		"null":                `null`,
		"array":               `[]`,
		"primitive":           `true`,
		"missing id":          `{"ok":true,"result":{}}`,
		"null id":             `{"id":null,"ok":true,"result":{}}`,
		"string id":           `{"id":"1","ok":true,"result":{}}`,
		"different id":        `{"id":2,"ok":true,"result":{}}`,
		"different number id": `{"id":1.0,"ok":true,"result":{}}`,
		"missing ok":          `{"id":1,"result":{}}`,
		"null ok":             `{"id":1,"ok":null,"result":{}}`,
		"wrong ok type":       `{"id":1,"ok":"true","result":{}}`,
		"unknown field":       `{"id":1,"ok":true,"result":{},"typo":true}`,
		"second JSON value":   `{"id":1,"ok":true,"result":{}} {}`,
		"trailing junk":       `{"id":1,"ok":true,"result":{}} x`,
		"missing result":      `{"id":1,"ok":true}`,
		"success with error":  `{"id":1,"ok":true,"result":{},"error":"failed"}`,
		"success null error":  `{"id":1,"ok":true,"result":{},"error":null}`,
		"success empty error": `{"id":1,"ok":true,"result":{},"error":""}`,
		"success with detail": `{"id":1,"ok":true,"result":{},"detail":"extra"}`,
		"success null detail": `{"id":1,"ok":true,"result":{},"detail":null}`,
		"failure no error":    `{"id":1,"ok":false}`,
		"failure null error":  `{"id":1,"ok":false,"error":null}`,
		"failure empty error": `{"id":1,"ok":false,"error":""}`,
		"failure number code": `{"id":1,"ok":false,"error":5}`,
		"failure null result": `{"id":1,"ok":false,"error":"failed","result":null}`,
		"failure result":      `{"id":1,"ok":false,"error":"failed","result":{}}`,
		"number detail":       `{"id":1,"ok":false,"error":"failed","detail":12}`,
		"null detail":         `{"id":1,"ok":false,"error":"failed","detail":null}`,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			address := replyFixture(t, response+"\n")
			if err := New(address).Call(callContext(t), "ping", nil, nil); !errors.Is(err, apiwire.ErrProtocol) {
				t.Fatalf("error = %v, want protocol error", err)
			}
		})
	}
}

func TestResultDecodeError(t *testing.T) {
	address := replyFixture(t, "{\"id\":1,\"ok\":true,\"result\":{\"count\":\"wrong type\"}}\n")
	var result struct{ Count int }
	err := New(address).Call(callContext(t), "example", nil, &result)
	var typed *json.UnmarshalTypeError
	if !errors.Is(err, apiwire.ErrProtocol) || !errors.As(err, &typed) {
		t.Fatalf("error = %v, want protocol error wrapping result type error", err)
	}
}

func TestResponseFraming(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           error
	}{
		{name: "empty EOF", want: io.EOF},
		{name: "unterminated", response: `{"id":1,"ok":true,"result":{}}`, want: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := replyFixture(t, tc.response)
			err := New(address).Call(callContext(t), "ping", nil, nil)
			if !errors.Is(err, apiwire.ErrProtocol) || !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want protocol error wrapping %v", err, tc.want)
			}
		})
	}
}

type spaces struct{}

func (spaces) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = ' '
	}
	return len(buffer), nil
}

func TestOversizeResponse(t *testing.T) {
	address := fixture(t, func(conn *net.TCPConn) {
		_ = readRequest(t, conn)
		// The client may close once the first over-limit fragment arrives.
		_, _ = io.CopyN(conn, spaces{}, apiwire.MaxResponseLine+1)
	})
	err := New(address).Call(callContext(t), "ping", nil, nil)
	if !errors.Is(err, apiwire.ErrProtocol) || !errors.Is(err, apiwire.ErrLineTooLarge) {
		t.Fatalf("error = %v, want protocol error wrapping line limit", err)
	}
}

func TestRequestBoundary(t *testing.T) {
	const overhead = len("{\"id\":1,\"method\":\"type_text\",\"params\":{\"text\":\"\"}}\n")
	text := strings.Repeat("x", apiwire.MaxRequestLine-overhead)
	address := fixture(t, func(conn *net.TCPConn) {
		line := readRequest(t, conn)
		if len(line) != apiwire.MaxRequestLine {
			t.Errorf("request length = %d, want %d including LF", len(line), apiwire.MaxRequestLine)
		}
		var request apiwire.Request
		if err := json.Unmarshal(line, &request); err != nil {
			t.Error(err)
		}
		var params struct{ Text string }
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Text != text {
			t.Errorf("params: text length %d, error %v", len(params.Text), err)
		}
		if _, err := io.WriteString(conn, "{\"id\":1,\"ok\":true,\"result\":{}}\n"); err != nil {
			t.Error(err)
		}
	})
	if err := New(address).Call(callContext(t), "type_text", map[string]string{"text": text}, nil); err != nil {
		t.Fatal(err)
	}
	// An invalid address makes an accidental Dial before the size check visible.
	err := New("invalid address").Call(callContext(t), "type_text", map[string]string{"text": text + "x"}, nil)
	if !errors.Is(err, apiwire.ErrRequestTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestLocalEncodingAndDialFailures(t *testing.T) {
	for name, params := range map[string]any{
		"unsupported type":  make(chan int),
		"unsupported value": math.NaN(),
		"invalid raw JSON":  json.RawMessage(`{`),
	} {
		t.Run(name, func(t *testing.T) {
			err := New("invalid address").Call(callContext(t), "example", params, nil)
			if err == nil || !strings.HasPrefix(err.Error(), "encode API request:") {
				t.Fatalf("error = %v, want encoding failure before dial", err)
			}
		})
	}
	err := New("invalid address").Call(callContext(t), "ping", nil, nil)
	var network *net.OpError
	if !errors.As(err, &network) || !strings.HasPrefix(err.Error(), "dial API:") {
		t.Fatalf("error = %v, want wrapped dial failure", err)
	}
}

func TestAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := New("invalid address").Call(ctx, "ping", make(chan int), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation before encoding/dial", err)
	}
}

func TestCallCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			started, closed := make(chan struct{}), make(chan struct{})
			address := fixture(t, func(conn *net.TCPConn) {
				_ = readRequest(t, conn)
				close(started)
				var one [1]byte
				if _, err := conn.Read(one[:]); !errors.Is(err, io.EOF) {
					t.Errorf("canceled client connection: %v, want EOF", err)
				}
				close(closed)
			})
			ctx, cancel := context.WithCancel(callContext(t))
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(callContext(t), 500*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- New(address).Call(ctx, "type_text", map[string]string{"text": "long action"}, nil) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("call did not reach server before its deadline")
			}
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not close the live connection")
			}
		})
	}
}

func TestWriteFailure(t *testing.T) {
	// net.Pipe makes the otherwise timing-dependent failed-write path exact;
	// cancellation, framing and concurrent calls use real TCP fixtures above/below.
	conn, peer := net.Pipe()
	_ = peer.Close()
	err := exchange(callContext(t), conn, []byte("request\n"), nil)
	if !errors.Is(err, io.ErrClosedPipe) || !strings.HasPrefix(err.Error(), "write API request:") {
		t.Fatalf("error = %v, want wrapped write failure", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	const calls = 32
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var servers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for range calls {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("accept: %v", err)
				}
				return
			}
			servers.Go(func() {
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Error(err)
					return
				}
				var request apiwire.Request
				if err := json.Unmarshal(readRequest(t, conn), &request); err != nil {
					t.Error(err)
					return
				}
				if string(request.ID) != "1" || request.Method != "echo" {
					t.Errorf("request = %+v", request)
				}
				response := apiwire.Response{ID: request.ID, OK: true, Result: request.Params}
				if err := json.NewEncoder(conn).Encode(response); err != nil {
					t.Error(err)
				}
			})
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-accepted
		servers.Wait()
	})
	client := New(listener.Addr().String())
	ctx := callContext(t)
	var clients sync.WaitGroup
	for i := range calls {
		clients.Go(func() {
			params := struct{ Index int }{Index: i}
			var result struct{ Index int }
			if err := client.Call(ctx, "echo", params, &result); err != nil || result != params {
				t.Errorf("call %d: result %v, error %v", i, result, err)
			}
		})
	}
	clients.Wait()
}
