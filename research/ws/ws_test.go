package ws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

type client struct {
	net.Conn
	br *bufio.Reader
}

func dial(t *testing.T, srv *httptest.Server, origin string) (*client, *http.Response) {
	t.Helper()
	nc, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	req := "GET /ws HTTP/1.1\r\nHost: " + srv.Listener.Addr().String() +
		"\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: permessage-deflate\r\nOrigin: " + origin + "\r\n\r\n"
	if _, err = io.WriteString(nc, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{nc, br}, resp
}

// frame writes a masked client frame.
func (c *client) frame(t *testing.T, fin bool, op byte, p []byte) {
	t.Helper()
	b := []byte{op, 0x80}
	if fin {
		b[0] |= 0x80
	}
	switch {
	case len(p) < 126:
		b[1] |= byte(len(p))
	case len(p) <= 0xffff:
		b[1] |= 126
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
	default:
		b[1] |= 127
		b = binary.BigEndian.AppendUint64(b, uint64(len(p)))
	}
	m := []byte{1, 2, 3, 4}
	b = append(b, m...)
	for i, x := range p {
		b = append(b, x^m[i&3])
	}
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

// read reads one unmasked server frame.
func (c *client) read(t *testing.T) (op byte, p []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	h := make([]byte, 2)
	if _, err := io.ReadFull(c.br, h); err != nil {
		t.Fatal(err)
	}
	if h[1]&0x80 != 0 || h[0]&0x80 == 0 {
		t.Fatalf("server frame masked or fragmented: % x", h)
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		_, _ = io.ReadFull(c.br, h)
		n = int(binary.BigEndian.Uint16(h))
	case 127:
		e := make([]byte, 8)
		_, _ = io.ReadFull(c.br, e)
		n = int(binary.BigEndian.Uint64(e))
	}
	p = make([]byte, n)
	if _, err := io.ReadFull(c.br, p); err != nil {
		t.Fatal(err)
	}
	return h[0] & 0x0f, p
}

func echoServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		c.MaxMessage = 64 << 10
		for {
			op, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err = c.WriteMessage(op, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptKeyRFCVector(t *testing.T) {
	if got := AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal(got)
	}
}

func TestHandshake(t *testing.T) {
	srv := echoServer(t)
	c, resp := dial(t, srv, srv.URL)
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" || resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	_ = c
	_, resp = dial(t, srv, "http://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin accepted", resp.StatusCode)
	}
	r, _ := http.Get(srv.URL + "/ws")
	if r.StatusCode != http.StatusBadRequest {
		t.Fatal("plain GET", r.StatusCode)
	}
	_ = r.Body.Close()
}

func TestMessages(t *testing.T) {
	srv := echoServer(t)
	c, _ := dial(t, srv, srv.URL)

	ev := []byte(`{"type":"keydown","code":"KeyA"}`)
	c.frame(t, true, OpText, ev)
	if op, p := c.read(t); op != OpText || !bytes.Equal(p, ev) {
		t.Fatalf("%d %q", op, p)
	}
	// Fragmented text with a ping in the middle; 16-bit length on one fragment.
	big := bytes.Repeat([]byte("x"), 300)
	c.frame(t, false, OpText, []byte("ab"))
	c.frame(t, true, opPing, []byte("hi"))
	c.frame(t, false, 0, big)
	c.frame(t, true, 0, []byte("cd"))
	if op, p := c.read(t); op != opPong || string(p) != "hi" {
		t.Fatalf("pong %d %q", op, p)
	}
	if op, p := c.read(t); op != OpText || string(p) != "ab"+string(big)+"cd" {
		t.Fatalf("reassembly %d %d", op, len(p))
	}
	// 64-bit length binary echo.
	bin := bytes.Repeat([]byte{1}, 0x10000)
	c.frame(t, true, OpBinary, bin)
	if op, p := c.read(t); op != OpBinary || !bytes.Equal(p, bin) {
		t.Fatalf("binary %d %d", op, len(p))
	}
	// Close handshake.
	c.frame(t, true, opClose, []byte{0x03, 0xe8})
	if op, p := c.read(t); op != opClose || binary.BigEndian.Uint16(p) != 1000 {
		t.Fatalf("close %d % x", op, p)
	}
}

func TestViolations(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(c *client)
		code uint16
	}{
		{"too big", func(c *client) { c.frame(t, true, OpBinary, make([]byte, 64<<10+1)) }, 1009},
		{"too big fragmented", func(c *client) {
			c.frame(t, false, OpBinary, make([]byte, 40<<10))
			c.frame(t, true, 0, make([]byte, 40<<10))
		}, 1009},
		{"unmasked", func(c *client) { _, _ = c.Write([]byte{0x81, 0x01, 'a'}) }, 1002},
		{"rsv1 (deflate)", func(c *client) { c.frame(t, true, 0x40|OpText, []byte("a")) }, 1002},
		{"bad utf8", func(c *client) { c.frame(t, true, OpText, []byte{0xff}) }, 1007},
		{"orphan continuation", func(c *client) { c.frame(t, true, 0, []byte("a")) }, 1002},
		{"fragmented ping", func(c *client) { c.frame(t, false, opPing, nil) }, 1002},
		{"reserved opcode", func(c *client) { c.frame(t, true, 3, nil) }, 1002},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := echoServer(t)
			c, _ := dial(t, srv, srv.URL)
			tc.send(c)
			if op, p := c.read(t); op != opClose || binary.BigEndian.Uint16(p) != tc.code {
				t.Fatalf("got %d % x want close %d", op, p, tc.code)
			}
			if _, err := c.br.ReadByte(); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) { // RST if unread data
				t.Fatal("server did not close TCP:", err)
			}
		})
	}
}

// A client that never reads must not block the producer or grow memory:
// Latest drops frames, and the stalled write times out and closes.
func TestSlowClient(t *testing.T) {
	video := NewLatest()
	res := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		c.WriteTimeout = 300 * time.Millisecond
		res <- Stream(c, video, nil, nil)
	}))
	defer srv.Close()
	c, _ := dial(t, srv, srv.URL)
	_ = c
	frame := append([]byte{1}, make([]byte, 256<<10)...)
	dropped := 0
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		if video.Put(frame) {
			dropped++
		}
		time.Sleep(time.Millisecond)
		select {
		case err := <-res:
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				t.Fatal("want timeout, got", err)
			}
			t.Logf("write timed out after %v, %d frames dropped, producer never blocked", time.Since(start).Round(time.Millisecond), dropped)
			return
		default:
		}
	}
	t.Fatal("stalled writer never timed out")
}

// Interop with an independent client: Node's built-in (undici, WHATWG) WebSocket.
func TestNodeInterop(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	srv := echoServer(t)
	script := `
const ws = new WebSocket(process.argv[1]); ws.binaryType = "arraybuffer";
const want = ["h\u00e9llo", 60000]; let i = 0;
ws.onopen = () => { ws.send(JSON.stringify({type:"mousemove",x:1,y:2})); ws.send("h\u00e9llo"); ws.send(new Uint8Array(60000)); };
ws.onmessage = (e) => {
  if (i === 0) { if (JSON.parse(e.data).type !== "mousemove") throw "json"; }
  else if (i === 1) { if (e.data !== want[0]) throw "text"; }
  else { if (e.data.byteLength !== want[1]) throw "bin"; ws.close(1000); }
  i++;
};
ws.onclose = (e) => { console.log(i === 3 && e.code === 1000 ? "ok" : "bad " + e.code); };
ws.onerror = (e) => { console.log("error", e.message); };
`
	out, err := exec.Command("node", "-e", script, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v\n%s", err, out)
	}
}
