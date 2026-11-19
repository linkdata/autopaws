package avfoundation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// process retains ownership of the child and its reader until the stream
// worker exits and Wait has reaped the child. Startup cancellation never
// becomes the lifetime context of an already returned stream.
type process struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startProcess(ctx context.Context, kind string, command func(context.Context) *exec.Cmd,
	read func(io.Reader, func()) error, finish func(error),
) (*process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	cmd := command(lifetime)
	cmd.WaitDelay = time.Second
	var diagnostics boundedBuffer
	cmd.Stderr = &diagnostics
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, errors.Join(err, pipe.Close())
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.Join(err, pipe.Close())
	}
	owner := &process{cancel: cancel, done: make(chan struct{})}
	ready := make(chan error, 1)
	var once sync.Once
	go func() {
		defer close(owner.done)
		err := read(pipe, func() { once.Do(func() { ready <- nil }) })
		// Stop before Wait even for malformed output; otherwise the child may
		// remain blocked writing to a pipe that no worker will drain.
		cancel()
		_ = pipe.Close()
		err = errors.Join(err, cmd.Wait())
		err = fmt.Errorf("avfoundation %s: %w: %s", kind, err, diagnostics.String())
		once.Do(func() { ready <- err })
		finish(err)
	}()
	select {
	case err := <-ready:
		if err == nil && ctx.Err() == nil {
			return owner, nil
		}
		owner.close()
		return nil, errors.Join(ctx.Err(), err)
	case <-ctx.Done():
		owner.close()
		return nil, ctx.Err()
	}
}

func (owner *process) close() {
	owner.cancel()
	<-owner.done
}
