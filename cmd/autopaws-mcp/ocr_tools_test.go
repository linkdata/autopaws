package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecuteAndReadCancelsWaitBeforeCapture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		typed := make(chan struct{})
		owner := &toolServer{caller: unitCaller(func(_ context.Context, method string, _, output any) error {
			if method != "type_text" {
				t.Errorf("cancelled wait reached %q", method)
			}
			close(typed)
			return unitAssign(output, map[string]bool{"typed": true})
		})}
		done := make(chan error, 1)
		go func() { _, err := owner.executeAndRead(ctx, nil, executeInput{Command: "hello"}); done <- err }()
		<-typed
		synctest.Wait() // The command finished and the handler is waiting on its timer.
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("wait cancellation=%v", err)
		}
	})
}

func TestExecuteAndReadRejectsBeforeTypingAndPreservesInputFailure(t *testing.T) {
	invalid := -1.0
	owner := &toolServer{caller: unitCaller(func(context.Context, string, any, any) error {
		t.Fatal("invalid wait typed input")
		return nil
	})}
	if _, err := owner.executeAndRead(t.Context(), &mcp.CallToolRequest{}, executeInput{WaitSeconds: &invalid}); err == nil {
		t.Fatal("invalid wait accepted")
	}
	failure := errors.New("input unavailable")
	owner.caller = unitCaller(func(context.Context, string, any, any) error { return failure })
	zero := 0.0
	if _, err := owner.executeAndRead(t.Context(), nil, executeInput{WaitSeconds: &zero}); !errors.Is(err, failure) {
		t.Fatalf("input failure lost=%v", err)
	}
}
