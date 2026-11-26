package capture

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
	"testing"
)

func frameMetadata(width, height int) Metadata {
	return Metadata{Width: width, Height: height, Generation: 7, TimestampNS: 123456000, TimestampFlags: 0x12001, Sequence: 42}
}

func TestColdFrameSnapshotCropsAndCaches(t *testing.T) {
	data := encodedImage(t, lumaImage(image.Rect(0, 0, 160, 120), image.Rect(0, 16, 160, 104), 200))
	metadata := frameMetadata(160, 120)
	frame, err := newProcessor(true, 160, 88).process(data, metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Crop = image.Rect(0, 16, 160, 104)
	if got := frame.Metadata(); got != metadata {
		t.Fatalf("metadata=%+v want%+v", got, metadata)
	}
	if !bytes.Equal(frame.JPEG(), data) {
		t.Fatal("web JPEG was encoded or cropped")
	}
	decoded, err := frame.decoded()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := frame.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Width != 160 || snapshot.Height != 88 || snapshot.Generation != 7 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(snapshot.JPEG))
	if err != nil || config.Width != 160 || config.Height != 88 {
		t.Fatalf("cropped JPEG=%+v,%v", config, err)
	}
	if again, err := frame.decoded(); err != nil || again != decoded {
		t.Fatal("decode was not shared with snapshot")
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			again, err := frame.Snapshot()
			if err != nil || &again.JPEG[0] != &snapshot.JPEG[0] {
				t.Error("snapshot was encoded more than once")
			}
		})
	}
	group.Wait()
}

func TestSnapshotPassthroughStillRequiresFullDecode(t *testing.T) {
	data := colorJPEG(t)
	processor := newProcessor(false, 0, 0)
	frame, err := processor.process(data, frameMetadata(160, 120), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := frame.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Width != 160 || snapshot.Height != 120 || &snapshot.JPEG[0] != &data[0] {
		t.Fatal("uncropped JPEG was unnecessarily re-encoded")
	}
	// The header remains valid after entropy data is truncated. Once warm, web
	// passthrough need not decode every frame, but a snapshot must reject this.
	truncated := data[:len(data)-40]
	if _, err := jpeg.DecodeConfig(bytes.NewReader(truncated)); err != nil {
		t.Fatal("bad fixture header", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(truncated)); err == nil {
		t.Fatal("bad fixture entropy unexpectedly decodes")
	}
	frame, err = processor.process(truncated, frameMetadata(160, 120), false)
	if err != nil {
		t.Fatal("warm web path required a full decode", err)
	}
	if _, err = frame.Snapshot(); err == nil {
		t.Fatal("truncated passthrough returned as a snapshot")
	}
	if _, err = processor.process(truncated, frameMetadata(160, 120), true); err == nil {
		t.Fatal("snapshot candidate accepted without full decode")
	}
	if _, err = newProcessor(false, 0, 0).process(truncated, frameMetadata(160, 120), false); err == nil {
		t.Fatal("corrupt warmup accepted")
	}
}

func TestProcessorValidatesMetadataAndRecoversFromCorruptWarmup(t *testing.T) {
	processor := newProcessor(true, 160, 88)
	for _, data := range [][]byte{nil, []byte("garbage"), {0xff, 0xd8, 0xff, 0xe0, 0}} {
		if _, err := processor.process(data, frameMetadata(160, 120), true); err == nil {
			t.Fatal("corrupt candidate accepted")
		}
	}
	data := colorJPEG(t)
	for _, metadata := range []Metadata{frameMetadata(0, 120), frameMetadata(160, -1), frameMetadata(159, 120), frameMetadata(160, 119)} {
		if _, err := processor.process(data, metadata, true); err == nil {
			t.Fatal("mismatched metadata accepted")
		}
	}
	frame, err := processor.process(withoutDHT(t, data), frameMetadata(160, 120), true)
	if err != nil {
		t.Fatal("valid frame did not repair startup", err)
	}
	snapshot, err := frame.Snapshot()
	if err != nil || !bytes.Equal(snapshot.JPEG, data) {
		t.Fatal("normalization was not shared with snapshot", err)
	}
}

func TestCropSamplingAndFrozenFrameMetadata(t *testing.T) {
	plain := encodedImage(t, lumaImage(image.Rect(0, 0, 160, 120), image.Rect(0, 0, 160, 120), 200))
	letterbox := encodedImage(t, lumaImage(image.Rect(0, 0, 160, 120), image.Rect(0, 16, 160, 104), 200))
	p := newProcessor(true, 160, 88)
	old, err := p.process(plain, frameMetadata(160, 120), false)
	if err != nil {
		t.Fatal(err)
	}
	// A pending snapshot detects a newly letterboxed image before frame30
	// while no crop has yet been found.
	fresh, err := p.process(letterbox, frameMetadata(160, 120), true)
	if err != nil || fresh.Metadata().Crop != image.Rect(0, 16, 160, 104) {
		t.Fatal("snapshot did not detect newly visible borders", err)
	}
	if !old.Metadata().Crop.Empty() {
		t.Fatal("old frame metadata changed")
	}
	for index := 3; index < 30; index++ {
		frame, err := p.process(plain, frameMetadata(160, 120), false)
		if err != nil || frame.Metadata().Crop.Empty() {
			t.Fatalf("crop sampled before frame30 (%d): %v", index, err)
		}
	}
	changed, err := p.process(plain, frameMetadata(160, 120), false)
	if err != nil || !changed.Metadata().Crop.Empty() {
		t.Fatal("frame30 did not reset obsolete crop", err)
	}
	if fresh.Metadata().Crop != image.Rect(0, 16, 160, 104) {
		t.Fatal("new detection rewrote published metadata")
	}
	// A new processor is a fresh stream generation, with its own detector.
	restarted, err := newProcessor(true, 160, 88).process(plain, frameMetadata(160, 120), true)
	if err != nil || !restarted.Metadata().Crop.Empty() {
		t.Fatal("new source inherited old crop", err)
	}
}

func TestSnapshotRejectsInvalidCrop(t *testing.T) {
	frame, err := newProcessor(false, 0, 0).process(colorJPEG(t), frameMetadata(160, 120), false)
	if err != nil {
		t.Fatal(err)
	}
	frame.metadata.Crop = image.Rect(-1, 0, 160, 120)
	if _, err := frame.Snapshot(); err == nil {
		t.Fatal("invalid crop accepted")
	}
}

func BenchmarkSnapshotEncode(b *testing.B) {
	data := encodedImage(b, lumaImage(image.Rect(0, 0, 1920, 1080), image.Rect(0, 140, 1920, 940), 160))
	b.ReportAllocs()
	for b.Loop() {
		frame := &Frame{metadata: Metadata{Width: 1920, Height: 1080, Crop: image.Rect(0, 140, 1920, 940)}, jpeg: data}
		if _, err := frame.Snapshot(); err != nil {
			b.Fatal(err)
		}
	}
}
