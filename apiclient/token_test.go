package apiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/linkdata/autopaws/apiwire"
)

func TestTokenOnEveryConnection(t *testing.T) {
	for _, secret := range []string{"", "a+;å% secret"} {
		for range 2 {
			address := fixture(t, func(conn *net.TCPConn) {
				line := readRequest(t, conn)
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(line, &fields); err != nil {
					t.Error(err)
					return
				}
				if secret == "" {
					if _, exists := fields["token"]; exists {
						t.Error("disabled authentication changed the request envelope")
					}
				} else {
					var supplied string
					if err := json.Unmarshal(fields["token"], &supplied); err != nil || supplied != secret {
						t.Errorf("request token mismatch: %v", err)
					}
				}
				_, _ = fmt.Fprintln(conn, `{"id":1,"ok":true,"result":{}}`)
			})
			if err := NewWithToken(address, secret).Call(callContext(t), "ping", nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestUnauthorizedIdentity(t *testing.T) {
	address := replyFixture(t, "{\"id\":1,\"ok\":false,\"error\":\"unauthorized\"}\n")
	if err := NewWithToken(address, "wrong").Call(callContext(t), "ping", nil, nil); !errors.Is(err, apiwire.ErrUnauthorized) {
		t.Fatalf("unauthorized response = %v", err)
	}
}
