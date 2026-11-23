//go:build (linux || darwin || windows) && (amd64 || arm64)

package ffmpegmedia

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMediaReaderAllowsProgressAndReportsStall(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	reader := mediaReader{input: input, timeout: 50 * time.Millisecond, interrupt: func() { _ = output.Close() }}
	for _, want := range []byte{'a', 'b'} {
		if _, err := output.Write([]byte{want}); err != nil {
			t.Fatal(err)
		}
		var data [1]byte
		if n, err := reader.Read(data[:]); err != nil || n != 1 || data[0] != want {
			t.Fatalf("read after progress: %d %q %v", n, data, err)
		}
	}
	var data [1]byte
	if _, err := reader.Read(data[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled pipe did not report its deadline: %v", err)
	}
}

func TestDiagnosticsBounded(t *testing.T) {
	var output Diagnostics
	data := make([]byte, 100<<10)
	for index := range data {
		data[index] = 'x'
	}
	if n, err := output.Write(data); err != nil || n != len(data) || len(output.String()) != 64<<10 || !output.Truncated() {
		t.Fatalf("bounded diagnostics: n=%d, error=%v, length=%d, truncated=%t", n, err, len(output.String()), output.Truncated())
	}
}

func TestProcessReapsStalledChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), mediaReadTimeout+5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	finished := make(chan error, 1)
	owner, err := startProcess(ctx, "fixture", audioHelper("first", &cmd), func(pipe io.Reader, ready func()) error {
		var first [1]byte
		if _, err := io.ReadFull(pipe, first[:]); err != nil {
			return err
		}
		ready()
		_, err := io.Copy(io.Discard, pipe)
		return err
	}, func(err error) { finished <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	select {
	case <-owner.done:
	case <-ctx.Done():
		t.Fatal("stalled process was not stopped and reaped")
	}
	if err := <-finished; !errors.Is(err, os.ErrDeadlineExceeded) || cmd.ProcessState == nil {
		t.Fatalf("stalled process result = %v, state = %v", err, cmd.ProcessState)
	}
}

func TestProcessGracefulStopAndReopen(t *testing.T) {
	for range 2 {
		var cmd *exec.Cmd
		owner, err := startProcess(t.Context(), "fixture", audioHelper("graceful", &cmd), func(pipe io.Reader, ready func()) error {
			var first [1]byte
			if _, err := io.ReadFull(pipe, first[:]); err != nil {
				return err
			}
			ready()
			_, err := io.Copy(io.Discard, pipe)
			return err
		}, func(error) {})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(owner.close)
		closed := make(chan struct{})
		go func() {
			owner.close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("graceful close did not join the child")
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
			t.Fatalf("graceful fixture did not receive q before stopping: %v", cmd.ProcessState)
		}
	}
}

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
