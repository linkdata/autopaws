package avfoundation

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestProcessRejectsCanceledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	command := func(context.Context) *exec.Cmd {
		called = true
		return nil
	}
	if owner, err := startProcess(ctx, "fixture", command, nil, nil); owner != nil || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("already canceled startup = %v, %v; command called %t", owner, err, called)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	var cmd *exec.Cmd
	command = func(lifetime context.Context) *exec.Cmd {
		cmd = exec.CommandContext(lifetime, os.Args[0], "-test.run=^TestAudioHelperProcess$")
		cancel() // Cancellation while constructing the command must precede Start.
		return cmd
	}
	worker := func(io.Reader, func()) error {
		t.Error("worker ran for canceled startup")
		return nil
	}
	if owner, err := startProcess(ctx, "fixture", command, worker, nil); owner != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled during construction = %v, %v", owner, err)
	}
	if cmd.Process != nil {
		t.Fatal("process started after startup cancellation")
	}
}
