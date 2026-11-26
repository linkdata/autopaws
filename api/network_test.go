//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestPendingRequestTimeout(t *testing.T) {
	for _, partial := range []string{"", "{"} {
		t.Run(partial, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				server, controller, _ := bareServer(t, Options{Token: "secret"})
				defer controller.Close()
				serverConn, client := net.Pipe()
				defer client.Close()
				started := time.Now()
				done := make(chan struct{})
				go func() {
					server.handleConn(t.Context(), serverConn)
					close(done)
				}()
				if partial != "" {
					writeTest(t, client, partial)
				}
				if _, err := io.Copy(io.Discard, client); err != nil {
					t.Fatalf("pending connection did not close cleanly: %v", err)
				}
				<-done
				if elapsed := time.Since(started); elapsed != requestTimeout {
					t.Fatalf("pending deadline = %v; want %v", elapsed, requestTimeout)
				}
			})
		})
	}
}

func TestPendingLimitDisplacesOldestAndAdmitsAuthenticatedCall(t *testing.T) {
	for _, secret := range []string{"", "secret"} {
		t.Run(secret, func(t *testing.T) {
			server, controller, _ := bareServer(t, Options{Token: secret})
			started, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			server.handlers["slow"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return struct{}{}, ctx.Err()
			}
			address, _ := serveForTest(t, server, controller)
			t.Cleanup(unblock)
			active := dialTest(t, address)
			writeTest(t, active, `{"method":"slow","token":"`+secret+`"}`+"\n")
			<-started
			var oldest net.Conn
			for range maxPending {
				conn := dialTest(t, address)
				writeTest(t, conn, "{")
				if oldest == nil {
					oldest = conn
				}
			}
			conn := dialTest(t, address)
			writeTest(t, conn, `{"method":"ping","token":"`+secret+`"}`+"\n")
			if response := replyTest(t, conn); !response.OK {
				t.Fatalf("pending sockets locked out authenticated call: %+v", response)
			}
			if _, err := oldest.Read(make([]byte, 1)); err == nil {
				t.Fatal("oldest pending socket was not closed")
			} else {
				var networkErr net.Error
				if errors.As(err, &networkErr) && networkErr.Timeout() {
					t.Fatalf("oldest pending socket was not closed: %v", err)
				}
			}
			unblock()
			if response := replyTest(t, active); !response.OK {
				t.Fatalf("pending admission interrupted an authenticated call: %+v", response)
			}
			server.mu.Lock()
			pending := len(server.pending)
			server.mu.Unlock()
			if pending != maxPending-1 {
				t.Fatalf("pending count = %d; want %d", pending, maxPending-1)
			}
		})
	}
}

func TestAuthenticationClearsRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, controller, _ := bareServer(t, Options{Token: "secret"})
		defer controller.Close()
		server.handlers["slow"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
			time.Sleep(2 * requestTimeout)
			return struct{}{}, ctx.Err()
		}
		serverConn, client := net.Pipe()
		defer client.Close()
		server.pending = append(server.pending, serverConn)
		done := make(chan struct{})
		go func() {
			server.handleConn(t.Context(), serverConn)
			close(done)
		}()
		writeTest(t, client, "{\"method\":\"slow\",\"token\":\"secret\"}\n")
		if response := replyTest(t, client); !response.OK {
			t.Fatalf("authentication retained request deadline: %+v", response)
		}
		<-done
	})
}

type retryListener struct {
	net.Listener
	failures int
}

func (listener *retryListener) Accept() (net.Conn, error) {
	if listener.failures > 0 {
		listener.failures--
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	return listener.Listener.Accept()
}

func TestServeRetriesTemporaryAcceptFailure(t *testing.T) {
	server, controller, _ := bareServer(t, Options{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, &retryListener{Listener: listener, failures: 2}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve shutdown: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("controller shutdown: %v", err)
		}
		server.Wait()
	})
	conn := dialTest(t, listener.Addr().String())
	writeTest(t, conn, "{\"method\":\"ping\"}\n")
	if response := replyTest(t, conn); !response.OK {
		t.Fatalf("temporary accept failure stopped API: %+v", response)
	}
}

func TestAcceptBackoffIsBoundedAndCancellationInterruptsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, controller, _ := bareServer(t, Options{})
		defer controller.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		var attempts []time.Time
		listener := functionListener{accept: func() (net.Conn, error) {
			attempts = append(attempts, time.Now())
			return nil, &net.OpError{Op: "accept", Err: syscall.EMFILE}
		}}
		started := time.Now()
		if err := server.Serve(ctx, listener); err != nil {
			t.Fatal(err)
		}
		server.Wait()
		if elapsed := time.Since(started); elapsed != 3*time.Second {
			t.Fatalf("cancellation did not interrupt accept backoff: %v", elapsed)
		}
		if len(attempts) != 10 {
			t.Fatalf("accept attempts = %d; want 10", len(attempts))
		}
		for i := 1; i < len(attempts); i++ {
			want := min(5*time.Millisecond*time.Duration(1<<(i-1)), time.Second)
			if delay := attempts[i].Sub(attempts[i-1]); delay != want {
				t.Fatalf("accept retry %d delay = %v; want %v", i, delay, want)
			}
		}
	})
}
