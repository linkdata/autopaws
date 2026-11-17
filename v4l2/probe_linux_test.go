//go:build linux && (amd64 || arm64)

package v4l2

import (
	"context"
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
)

func probeOperations(fake *fakeCapture, ops operations) operations {
	ops.open = func(path string, flags int) (file, error) {
		if path != "/probe/video" || flags != os.O_RDONLY|syscall.O_NONBLOCK {
			panic("probe did not use a read-only nonblocking open")
		}
		return fake.file, nil
	}
	return ops
}

func TestProbeIsQueryOnlyAndIncludesNonMJPGCapture(t *testing.T) {
	fake, ops := fakeOperations()
	fake.formats = []uint32{0x56595559}
	fake.caps.DeviceCaps = CapVideoCapture // No streaming/MJPG requirement.
	fake.file.deadlineErr = errors.New("deadlines are not needed for probing")
	info, err := probe(context.Background(), "/probe/video", probeOperations(fake, ops))
	if err != nil {
		t.Fatal(err)
	}
	if info.Capabilities != fake.caps.Capabilities || info.DeviceCaps != CapVideoCapture || len(info.Formats) != 1 || info.Formats[0].FourCC != 0x56595559 {
		t.Fatalf("info=%+v", info)
	}
	events, mapped, _ := fake.snapshot()
	if !reflect.DeepEqual(events, []string{"QUERYCAP", "ENUM_FMT0", "ENUM_FMT1"}) || mapped != 0 || fake.file.closes != 1 {
		t.Fatalf("probe mutated capture state: events=%v maps=%d closes=%d", events, mapped, fake.file.closes)
	}
	for _, caps := range []capability{
		{Capabilities: CapDeviceCaps | CapVideoCapture, DeviceCaps: 0x00800000},
		{Capabilities: 0x00800000},
	} {
		fake, ops := fakeOperations()
		fake.caps = caps
		info, err := probe(context.Background(), "/probe/video", probeOperations(fake, ops))
		if err != nil || len(info.Formats) != 0 || info.Capabilities != caps.Capabilities || info.DeviceCaps != caps.DeviceCaps {
			t.Fatalf("metadata info=%+v,%v", info, err)
		}
		if events, _, _ := fake.snapshot(); !reflect.DeepEqual(events, []string{"QUERYCAP"}) {
			t.Fatalf("metadata node format enumeration: %v", events)
		}
	}
}

func TestProbeSkipsFormatQueryWithoutDeviceSpecificCapabilities(t *testing.T) {
	fake, ops := fakeOperations()
	fake.caps = capability{Capabilities: CapVideoCapture | capStreaming, DeviceCaps: CapVideoCapture}
	// ENUM_FMT must never run for a node discovery will exclude, even if that
	// query would fail. Streaming Open separately retains its legacy fallback.
	fake.fail = "ENUM_FMT0"
	info, err := probe(context.Background(), "/probe/video", probeOperations(fake, ops))
	if err != nil || len(info.Formats) != 0 || info.Capabilities != fake.caps.Capabilities || info.DeviceCaps != fake.caps.DeviceCaps {
		t.Fatalf("legacy capability probe=%+v,%v", info, err)
	}
	if events, mapped, _ := fake.snapshot(); !reflect.DeepEqual(events, []string{"QUERYCAP"}) || mapped != 0 || fake.file.closes != 1 {
		t.Fatalf("filtered node received format query: %v maps=%d closes=%d", events, mapped, fake.file.closes)
	}
}

func TestProbeFailuresRetainCausesAndCloseOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe(ctx, "unused", operations{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), t.TempDir()+"/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing probe=%v", err)
	}
	if _, err := Probe(context.Background(), "/dev/null"); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("/dev/null QUERYCAP=%v", err)
	}
	for _, stage := range []string{"raw", "QUERYCAP", "ENUM_FMT0", "close"} {
		t.Run(stage, func(t *testing.T) {
			fake, ops := fakeOperations()
			switch stage {
			case "raw":
				fake.file.rawErr = fake.failure
			case "close":
				fake.file.closeErr = fake.failure
			default:
				fake.fail = stage
			}
			if _, err := probe(context.Background(), "/probe/video", probeOperations(fake, ops)); !errors.Is(err, fake.failure) || fake.file.closes != 1 {
				t.Fatalf("probe error=%v closes=%d", err, fake.file.closes)
			}
		})
	}
}
