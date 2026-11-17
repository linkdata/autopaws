//go:build linux && (amd64 || arm64)

package v4l2

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type activatingFile struct {
	*fakeFile
	activateRead func() (syscall.RawConn, error)
}

func (file activatingFile) activate() (syscall.RawConn, error) { return file.activateRead() }

func TestNetpollActivationWaitsForStreamonAndFailureCleansQueues(t *testing.T) {
	for _, fail := range []bool{false, true} {
		fake, ops := fakeOperations()
		ops.open = func(string, int) (file, error) {
			return activatingFile{fake.file, func() (syscall.RawConn, error) {
				events, _, _ := fake.snapshot()
				if events[len(events)-1] != "STREAMON" {
					t.Fatalf("netpoll activated before successful STREAMON: %v", events)
				}
				if fail {
					return nil, fake.failure
				}
				return fake.file.raw, nil
			}}, nil
		}
		device, err := open(context.Background(), "/fake/video", Options{1920, 1080}, ops)
		if fail {
			if device != nil || !errors.Is(err, fake.failure) {
				t.Fatalf("activation failure=%v,%v", device, err)
			}
			if events, mapped, unmapped := fake.snapshot(); mapped != unmapped || fake.file.closes != 1 || countEvent(events, "STREAMOFF") != 1 {
				t.Fatalf("activation leaked queued resources: %v maps=%d unmaps=%d closes=%d", events, mapped, unmapped, fake.file.closes)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if err := device.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRawFileTransfersOwnershipToPollableFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	raw, err := reader.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var duplicate int
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) {
		duplicate, duplicateErr = syscall.Dup(int(fd))
	}); err != nil || duplicateErr != nil {
		t.Fatalf("duplicate generic pipe=%v/%v", err, duplicateErr)
	}
	syscall.CloseOnExec(duplicate)
	file := &streamFile{fd: duplicate, path: "generic-pipe"}
	defer file.Close()
	setup, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := setup.Read(func(uintptr) bool { return true }); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("unregistered read=%v", err)
	}
	if err := setup.Write(func(uintptr) bool { return true }); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("unregistered write=%v", err)
	}
	polled, err := file.activate()
	if err != nil || file.fd != -1 || file.file == nil {
		t.Fatalf("activation=%v, fd=%d file=%v", err, file.fd, file.file)
	}
	if err := file.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	var value [1]byte
	if err := polled.Read(func(fd uintptr) bool {
		_, err = syscall.Read(int(fd), value[:])
		return !errors.Is(err, syscall.EAGAIN)
	}); err != nil || value[0] != 'x' {
		t.Fatalf("registered pipe read=%q,%v", value, err)
	}
	if conn, err := file.SyscallConn(); conn == nil || err != nil {
		t.Fatalf("published SyscallConn=%v,%v", conn, err)
	}
}

func TestReadDeadlineResetFailureIsTerminal(t *testing.T) {
	device, fake := openFake(t)
	fake.file.deadlineErr = fake.failure
	if _, err := device.ReadFrame(context.Background()); !errors.Is(err, fake.failure) || !strings.Contains(err.Error(), "reset read deadline") {
		t.Fatalf("deadline failure=%v", err)
	}
	fake.file.deadlineErr = nil
	if _, err := device.ReadFrame(context.Background()); !errors.Is(err, fake.failure) {
		t.Fatalf("broken poll source resumed: %v", err)
	}
}
