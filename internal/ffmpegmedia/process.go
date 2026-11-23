// Package ffmpegmedia owns the shared FFmpeg pipes, frame decoding and PCM queues.
package ffmpegmedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const mediaReadTimeout = 5 * time.Second

var ErrClosed = errors.New("ffmpeg: stream closed")

// A stalled DirectShow or AVFoundation source can keep its pipe open. Windows
// anonymous pipes do not support read deadlines, so expiry stops the owner.
type mediaReader struct {
	input     io.Reader
	timeout   time.Duration
	interrupt func()
}

func (reader mediaReader) Read(data []byte) (int, error) {
	interrupted := make(chan struct{})
	timer := time.AfterFunc(reader.timeout, func() {
		defer close(interrupted)
		reader.interrupt()
	})
	n, err := reader.input.Read(data)
	if timer.Stop() {
		return n, err
	}
	<-interrupted
	return n, errors.Join(os.ErrDeadlineExceeded, err)
}

// process retains the child until its reader exits and Wait reaps it. Startup
// cancellation never becomes the lifetime context of a returned stream.
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
	ConfigureCommand(cmd)
	cmd.WaitDelay = time.Second
	var diagnostics Diagnostics
	cmd.Stderr = &diagnostics
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, err
	}
	stdin, control, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stdin = stdin
	cmd.Cancel = func() error {
		// This private pipe receives only these two bytes. FFmpeg can stop its
		// capture graph cleanly; WaitDelay still kills an unresponsive child.
		_, err := control.Write([]byte("q\n"))
		return errors.Join(err, control.Close())
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.Join(err, stdin.Close(), control.Close())
	}
	err = cmd.Start()
	_ = stdin.Close()
	if err != nil {
		cancel()
		return nil, errors.Join(err, pipe.Close(), control.Close())
	}
	owner := &process{cancel: cancel, done: make(chan struct{})}
	ready := make(chan error, 1)
	var once sync.Once
	go func() {
		defer close(owner.done)
		defer control.Close()
		err := read(mediaReader{input: pipe, timeout: mediaReadTimeout, interrupt: func() {
			cancel()
			_ = pipe.Close()
		}}, func() { once.Do(func() { ready <- nil }) })
		// Stop before Wait even for malformed output, otherwise the child may
		// stay blocked writing to a pipe no worker will drain.
		cancel()
		_ = pipe.Close()
		err = errors.Join(err, cmd.Wait())
		err = fmt.Errorf("ffmpeg %s: %w: %s", kind, err, diagnostics.String())
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

// Diagnostics bounds retained process output without blocking its writer.
type Diagnostics struct {
	data      []byte
	truncated bool
}

func (buffer *Diagnostics) Write(data []byte) (int, error) {
	n := len(data)
	const limit = 64 << 10
	left := max(0, limit-len(buffer.data))
	buffer.data = append(buffer.data, data[:min(left, n)]...)
	buffer.truncated = buffer.truncated || n > left
	return n, nil
}

func (buffer *Diagnostics) String() string  { return strings.TrimSpace(string(buffer.data)) }
func (buffer *Diagnostics) Truncated() bool { return buffer.truncated }
