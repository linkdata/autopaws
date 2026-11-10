// Package ws is a minimal server-side RFC 6455 WebSocket prototype.
//
// Supported: handshake, same-origin check, unfragmented and fragmented
// text/binary messages, ping/pong, close handshake, message size limit,
// UTF-8 validation of text. Not supported: extensions (permessage-deflate is
// never negotiated), subprotocols, client side.
package ws

import (
	"bufio"
	"crypto/sha1" // #nosec G505 -- mandated by RFC 6455 handshake
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Opcodes.
const (
	OpText   = 1
	OpBinary = 2
	opClose  = 8
	opPing   = 9
	opPong   = 10
)

// Close status codes used by the prototype.
const (
	closeNormal   = 1000
	closeProtocol = 1002
	closeBadData  = 1007
	closeTooBig   = 1009
)

// ErrProtocol reports a peer protocol violation.
var ErrProtocol = errors.New("websocket: protocol error")

// ErrTooBig reports a message over Conn.MaxMessage.
var ErrTooBig = errors.New("websocket: message too big")

// Conn is a server-side WebSocket connection.
//
// One goroutine may call ReadMessage while others call WriteMessage;
// writes are serialized internally.
type Conn struct {
	MaxMessage   int           // max reassembled message size; 0 means 1 MiB
	WriteTimeout time.Duration // per-frame write deadline; 0 means none
	nc           net.Conn
	br           *bufio.Reader
	mu           sync.Mutex // guards writes
	closeSent    bool
}

// Upgrade performs the server handshake and hijacks the connection.
// Cross-origin requests are rejected (Origin host must equal Host).
func Upgrade(w http.ResponseWriter, r *http.Request) (c *Conn, err error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	k, _ := base64.StdEncoding.DecodeString(key)
	switch {
	case r.Method != http.MethodGet,
		!headerHas(r.Header, "Connection", "upgrade"),
		!headerHas(r.Header, "Upgrade", "websocket"),
		r.Header.Get("Sec-WebSocket-Version") != "13",
		len(k) != 16:
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "bad websocket handshake", http.StatusBadRequest)
		return nil, ErrProtocol
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, perr := url.Parse(o); perr != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "cross-origin websocket", http.StatusForbidden)
			return nil, ErrProtocol
		}
	}
	var nc net.Conn
	var brw *bufio.ReadWriter
	if nc, brw, err = http.NewResponseController(w).Hijack(); err == nil {
		_ = nc.SetDeadline(time.Time{})
		_, err = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + AcceptKey(key) + "\r\n\r\n")
		if err == nil {
			if err = brw.Flush(); err == nil {
				return &Conn{nc: nc, br: brw.Reader}, nil
			}
		}
		_ = nc.Close()
	}
	return
}

// AcceptKey computes Sec-WebSocket-Accept for a Sec-WebSocket-Key.
func AcceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // #nosec G401 -- RFC 6455
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// ReadMessage returns the next text or binary message. Pings are answered
// and pongs ignored. A peer close is echoed and returns io.EOF. On protocol
// errors the connection is closed with the matching status code.
func (c *Conn) ReadMessage() (op int, msg []byte, err error) {
	limit := c.MaxMessage
	if limit <= 0 {
		limit = 1 << 20
	}
	for {
		var fin bool
		var fop int
		var payload []byte
		if fin, fop, payload, err = c.readFrame(limit - len(msg)); err != nil {
			return 0, nil, c.fail(err)
		}
		switch {
		case fop == opPing:
			err = c.writeFrame(opPong, payload)
		case fop == opPong:
		case fop == opClose:
			code := closeNormal
			if len(payload) == 1 {
				return 0, nil, c.fail(ErrProtocol)
			}
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			_ = c.closeWith(code)
			return 0, nil, io.EOF
		case fop == 0 && op == 0, fop != 0 && op != 0, fop > OpBinary:
			// continuation without start, new data frame mid-message, reserved opcode
			return 0, nil, c.fail(ErrProtocol)
		default:
			if fop != 0 {
				op = fop
			}
			msg = append(msg, payload...)
			if fin {
				if op == OpText && !utf8.Valid(msg) {
					return 0, nil, c.fail(errBadUTF8)
				}
				return
			}
		}
		if err != nil {
			return 0, nil, err
		}
	}
}

var errBadUTF8 = errors.New("websocket: invalid utf-8")

func (c *Conn) readFrame(room int) (fin bool, op int, payload []byte, err error) {
	var h [14]byte
	if _, err = io.ReadFull(c.br, h[:2]); err == nil {
		fin, op = h[0]&0x80 != 0, int(h[0]&0x0f)
		n := uint64(h[1] & 0x7f)
		switch {
		case h[0]&0x70 != 0, h[1]&0x80 == 0: // RSV bits set, or client frame not masked
			err = ErrProtocol
		case op >= opClose && (!fin || n > 125):
			err = ErrProtocol
		case n == 126:
			if _, err = io.ReadFull(c.br, h[2:4]); err == nil {
				n = uint64(binary.BigEndian.Uint16(h[2:4]))
			}
		case n == 127:
			if _, err = io.ReadFull(c.br, h[2:10]); err == nil {
				n = binary.BigEndian.Uint64(h[2:10])
			}
		}
		if err == nil && op < opClose && n > uint64(room) {
			err = ErrTooBig
		}
		if err == nil {
			var mask [4]byte
			if _, err = io.ReadFull(c.br, mask[:]); err == nil {
				payload = make([]byte, n)
				if _, err = io.ReadFull(c.br, payload); err == nil {
					for i := range payload {
						payload[i] ^= mask[i&3]
					}
				}
			}
		}
	}
	return
}

// fail closes the connection with a status derived from err and returns err.
func (c *Conn) fail(err error) error {
	code := closeProtocol
	switch {
	case errors.Is(err, ErrTooBig):
		code = closeTooBig
	case errors.Is(err, errBadUTF8):
		code = closeBadData
	case !errors.Is(err, ErrProtocol):
		_ = c.nc.Close() // I/O error: nothing to say
		return err
	}
	_ = c.closeWith(code)
	return err
}

func (c *Conn) closeWith(code int) (err error) {
	c.mu.Lock()
	if !c.closeSent {
		c.closeSent = true
		_ = c.nc.SetWriteDeadline(time.Now().Add(time.Second))
		err = c.writeLocked(opClose, binary.BigEndian.AppendUint16(nil, uint16(code))) // #nosec G115 -- codes < 65536
	}
	c.mu.Unlock()
	return errors.Join(err, c.nc.Close())
}

// WriteMessage sends one unfragmented message. It is safe for concurrent use.
func (c *Conn) WriteMessage(op int, data []byte) error {
	return c.writeFrame(op, data)
}

// Close sends a normal close frame and closes the connection.
func (c *Conn) Close() error { return c.closeWith(closeNormal) }

func (c *Conn) writeFrame(op int, data []byte) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeSent {
		return net.ErrClosed
	}
	if c.WriteTimeout > 0 {
		_ = c.nc.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
	}
	return c.writeLocked(op, data)
}

func (c *Conn) writeLocked(op int, data []byte) (err error) {
	h := make([]byte, 2, 10)
	h[0] = 0x80 | byte(op) // #nosec G115 -- opcode < 16
	switch n := len(data); {
	case n < 126:
		h[1] = byte(n)
	case n <= 0xffff:
		h[1] = 126
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h[1] = 127
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	bufs := net.Buffers{h, data} // writev: no copy of the JPEG
	_, err = bufs.WriteTo(c.nc)
	return
}
