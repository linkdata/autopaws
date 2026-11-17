//go:build linux && (amd64 || arm64)

package devices

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

func TestListFormatsRemainReadableForBigEndianFourCC(t *testing.T) {
	var output bytes.Buffer
	inventory := Inventory{Video: []VideoDevice{{Path: "/dev/video0", Name: "HDMI", Formats: []v4l2.PixelFormat{
		{FourCC: v4l2.PixFmtMJPEG, Description: "Motion-JPEG"},
		{FourCC: 'A' | 'R'<<8 | '1'<<16 | '5'<<24 | 1<<31, Description: "ARGB555X"},
		{FourCC: 0, Description: "Unknown"},
	}}}}
	if err := WriteList(&output, inventory); err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(output.Bytes()) || !strings.Contains(output.String(), "Formats: MJPG (Motion-JPEG), 0xB5315241 (ARGB555X), 0x00000000 (Unknown)") {
		t.Fatalf("unreadable FourCC listing: %q", output.String())
	}
}

type tree struct {
	t      *testing.T
	root   string
	probes map[string]v4l2.Info
	calls  []string
}

func newTree(t *testing.T) *tree {
	t.Helper()
	f := &tree{t: t, root: t.TempDir(), probes: make(map[string]v4l2.Info)}
	for _, path := range []string{"devices", "class/tty", "class/video4linux", "class/sound"} {
		f.dir(path)
	}
	return f
}

func (f *tree) dir(path string) string {
	f.t.Helper()
	path = filepath.Join(f.root, path)
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *tree) write(path, contents string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(contents), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *tree) link(path, target string) {
	f.t.Helper()
	if err := os.Symlink(target, filepath.Join(f.root, path)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *tree) usb(path, vid, pid string) string {
	f.t.Helper()
	f.dir(path)
	f.write(path+"/idVendor", vid)
	f.write(path+"/idProduct", pid)
	return path
}

func (f *tree) tty(name, parent string) {
	f.t.Helper()
	dir := parent + "/serial/" + name
	f.dir(dir)
	f.link(dir+"/device", filepath.Join(f.root, parent))
	f.link("class/tty/"+name, filepath.Join(f.root, dir))
}

func (f *tree) video(name, label, parent string, info v4l2.Info) {
	f.t.Helper()
	dir := parent + "/video4linux/" + name
	f.dir(dir)
	f.write(dir+"/name", label+"\n")
	f.link("class/video4linux/"+name, filepath.Join(f.root, dir))
	f.probes["/dev/"+name] = info
}

func (f *tree) card(name, id, parent string, pcms ...string) {
	f.t.Helper()
	dir := parent + "/sound/" + name
	f.dir(dir)
	f.write(dir+"/id", id+"\n")
	for _, pcm := range pcms {
		f.dir(dir + "/" + pcm)
	}
	f.link("class/sound/"+name, filepath.Join(f.root, dir))
}

func (f *tree) options() Options {
	return Options{SysRoot: f.root, Probe: func(_ context.Context, path string) (v4l2.Info, error) {
		f.calls = append(f.calls, path)
		info, ok := f.probes[path]
		if !ok {
			f.t.Errorf("unexpected video probe %q", path)
		}
		return info, nil
	}}
}

func captureInfo() v4l2.Info {
	return v4l2.Info{Capabilities: v4l2.CapDeviceCaps | v4l2.CapVideoCapture, DeviceCaps: v4l2.CapVideoCapture, Formats: []v4l2.PixelFormat{
		{FourCC: 'M' | 'J'<<8 | 'P'<<16 | 'G'<<24, Description: "Motion-JPEG", Flags: 1},
		{FourCC: 'Y' | 'U'<<8 | 'Y'<<16 | 'V'<<24, Description: "YUYV 4:2:2"},
	}}
}

func populatedTree(t *testing.T) *tree {
	t.Helper()
	f := newTree(t)
	serialUSB := f.usb("devices/usb1/1-13", "1A86\n", "7523")
	f.tty("ttyUSB0", serialUSB)
	other := f.usb("devices/usb1/1-9", "0403", "6001")
	f.tty("ttyUSB1", other)
	card := f.usb("devices/usb4/4-1", "2b89", "5389")
	f.video("video0", "UGREEN 15389", card+"/4-1:1.0", captureInfo())
	f.video("video1", "UGREEN metadata", card+"/4-1:1.0", v4l2.Info{Capabilities: v4l2.CapDeviceCaps | v4l2.CapVideoCapture})
	webcam := f.usb("devices/usb1/1-5", "0bda", "5510")
	f.video("video2", "Integrated_Webcam_HD", webcam+"/1-5:1.0", captureInfo())
	f.card("card1", "U15389", card+"/4-1:1.2", "pcmC1D0c", "pcmC1D0p")
	f.card("card0", "sofsoundwire", "devices/pci0000:00/audio", "pcmC0D0c")
	return f
}

func TestDiscoveryAndSelection(t *testing.T) {
	f := populatedTree(t)
	inventory, err := Discover(t.Context(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Serial) != 1 || inventory.Serial[0].Path != "/dev/ttyUSB0" || inventory.Serial[0].USB.VID != "1a86" {
		t.Fatalf("serial inventory = %+v", inventory.Serial)
	}
	if len(inventory.Video) != 2 || inventory.Video[0].Path != "/dev/video0" || inventory.Video[1].Path != "/dev/video2" || !reflect.DeepEqual(inventory.Video[0].Formats, captureInfo().Formats) {
		t.Fatalf("video inventory = %+v", inventory.Video)
	}
	if !slices.Equal(f.calls, []string{"/dev/video0", "/dev/video1", "/dev/video2"}) {
		t.Fatalf("probe order = %v", f.calls)
	}
	if len(inventory.Audio) != 2 || inventory.Audio[0].Path != "/dev/snd/pcmC0D0c" || inventory.Audio[0].USB.BusPath != "" || inventory.Audio[1].CardID != "U15389" {
		t.Fatalf("audio inventory = %+v", inventory.Audio)
	}
	video, err := SelectVideo(inventory.Video)
	if err != nil || video.Path != "/dev/video0" {
		t.Fatalf("default video = %+v, %v", video, err)
	}
	audio, found := AudioForVideo(video, inventory.Audio)
	if !found || audio.Path != "/dev/snd/pcmC1D0c" || video.USB.BusPath != audio.USB.BusPath {
		t.Fatalf("matched audio = %+v, %t", audio, found)
	}
	f.probes["/dev/video0"].Formats[0].Description = "mutated"
	if inventory.Video[0].Formats[0].Description != "Motion-JPEG" {
		t.Fatal("inventory aliases probe-owned format storage")
	}
}

func TestIdenticalCardsAndMultiplePCMs(t *testing.T) {
	f := populatedTree(t)
	second := f.usb("devices/usb4/4-2", "2b89", "5389")
	f.video("video10", "UGREEN 15389", second+"/4-2:1.0", captureInfo())
	f.card("card10", "U15389", second+"/4-2:1.2", "pcmC10D10c", "pcmC10D2c", "pcmC10D0p", "pcmC1D0c", "0c", "pcmC10Dx", "pcmC10D999999999999999999999c")
	ch341 := f.usb("devices/usb1/1-14", "1a86", "7522")
	f.tty("ttyUSB10", ch341)
	f.tty("ttyUSB2", ch341)
	inventory, err := Discover(t.Context(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	var serialPaths, audioPaths []string
	for _, device := range inventory.Serial {
		serialPaths = append(serialPaths, device.Path)
	}
	for _, device := range inventory.Audio {
		audioPaths = append(audioPaths, device.Path)
	}
	if !slices.Equal(serialPaths, []string{"/dev/ttyUSB0", "/dev/ttyUSB2", "/dev/ttyUSB10"}) || !slices.Equal(audioPaths, []string{"/dev/snd/pcmC0D0c", "/dev/snd/pcmC1D0c", "/dev/snd/pcmC10D2c", "/dev/snd/pcmC10D10c"}) {
		t.Fatalf("numeric order: serial %v audio %v", serialPaths, audioPaths)
	}
	firstAudio, found := AudioForVideo(inventory.Video[0], inventory.Audio)
	if !found || firstAudio.Path != "/dev/snd/pcmC1D0c" {
		t.Fatalf("first card audio = %+v, %t", firstAudio, found)
	}
	slices.Reverse(inventory.Audio)
	secondAudio, found := AudioForVideo(inventory.Video[2], inventory.Audio)
	if !found || secondAudio.Path != "/dev/snd/pcmC10D2c" || secondAudio.USB.BusPath == firstAudio.USB.BusPath {
		t.Fatalf("second card audio = %+v, %t", secondAudio, found)
	}
	if _, found := AudioForVideo(VideoDevice{}, inventory.Audio); found {
		t.Fatal("empty USB identity matched PCI audio")
	}
	if _, found := AudioForVideo(VideoDevice{USB: serial.USBDevice{BusPath: "/missing"}}, inventory.Audio); found {
		t.Fatal("unrelated USB identity matched audio")
	}
}

func TestVideoFilterAndNumericOrder(t *testing.T) {
	f := newTree(t)
	parent := "devices/platform/video"
	f.video("video10", "later HDMI", parent, captureInfo())
	f.video("video2", "Webcam", parent, captureInfo())
	f.video("video02", "first HDMI", parent, captureInfo())
	f.video("video3", "old-driver aggregate", parent, v4l2.Info{Capabilities: v4l2.CapVideoCapture, DeviceCaps: v4l2.CapVideoCapture})
	// Discovery deliberately retains capture-only, non-MJPG devices. Streaming
	// Open owns the separate STREAMING/MJPG requirements.
	f.video("video4", "YUYV only", parent, v4l2.Info{Capabilities: v4l2.CapDeviceCaps, DeviceCaps: v4l2.CapVideoCapture})
	for _, name := range []string{"video", "video+5", "videoX", "video99999999999999999999999999", "other0"} {
		f.dir("class/video4linux/" + name)
	}
	videos, err := DiscoverVideo(t.Context(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, video := range videos {
		paths = append(paths, video.Path)
		if video.USB.BusPath != "" {
			t.Fatal("non-USB video invented a USB ancestor")
		}
	}
	if !slices.Equal(paths, []string{"/dev/video02", "/dev/video2", "/dev/video4", "/dev/video10"}) {
		t.Fatalf("capture filter/order = %v", paths)
	}
}

func TestVideoNameSelection(t *testing.T) {
	for _, name := range []string{"WEBCAM", "usb Camera", "IR Camera", "front video", "rear video", "FaceTime HD"} {
		videos := []VideoDevice{{Path: "/dev/video0", Name: name}, {Path: "/dev/video2", Name: "HDMI"}}
		video, err := SelectVideo(videos)
		if err != nil || video.Path != "/dev/video2" {
			t.Fatalf("%q blocked HDMI selection: %+v, %v", name, video, err)
		}
	}
	video, err := SelectVideo([]VideoDevice{{Path: "/dev/video2", Name: "camera"}, {Path: "/dev/video10", Name: "webcam"}})
	if err != nil || video.Path != "/dev/video2" {
		t.Fatalf("all-webcam fallback = %+v, %v", video, err)
	}
	if _, err := SelectVideo(nil); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("empty video selection = %v", err)
	}
}

func TestMissingClassesAndInvalidRoots(t *testing.T) {
	f := newTree(t)
	for _, class := range []string{"video4linux", "sound"} {
		if err := os.RemoveAll(filepath.Join(f.root, "class", class)); err != nil {
			t.Fatal(err)
		}
	}
	if videos, err := DiscoverVideo(t.Context(), Options{SysRoot: f.root}); err != nil || len(videos) != 0 {
		t.Fatalf("no video subsystem = %v, %v", videos, err)
	}
	if audios, err := DiscoverAudio(t.Context(), f.root); err != nil || len(audios) != 0 {
		t.Fatalf("no sound subsystem = %v, %v", audios, err)
	}
	if rootPath("") != "/sys" || rootPath(f.root) != f.root {
		t.Fatal("wrong default sysfs root")
	}
	missing := filepath.Join(f.root, "missing")
	if _, err := DiscoverVideo(t.Context(), Options{SysRoot: missing}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bad video root = %v", err)
	}
	if _, err := DiscoverAudio(t.Context(), missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bad audio root = %v", err)
	}
}

func TestCanceledDiscovery(t *testing.T) {
	f := populatedTree(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checks := []func() error{
		func() error { _, err := Discover(ctx, f.options()); return err },
		func() error { _, err := DiscoverSerial(ctx, f.root); return err },
		func() error { _, err := DiscoverVideo(ctx, f.options()); return err },
		func() error { _, err := DiscoverAudio(ctx, f.root); return err },
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	options := f.options()
	options.Probe = func(context.Context, string) (v4l2.Info, error) {
		cancel()
		return captureInfo(), nil
	}
	if _, err := DiscoverVideo(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after probe lost: %v", err)
	}
}

func TestVideoDisappearingAndProbeFailures(t *testing.T) {
	for _, failure := range []error{fs.ErrNotExist, syscall.ENODEV, syscall.EACCES} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newTree(t)
			f.video("video0", "HDMI", "devices/platform/video", captureInfo())
			options := f.options()
			options.Probe = func(context.Context, string) (v4l2.Info, error) { return v4l2.Info{}, failure }
			videos, err := DiscoverVideo(t.Context(), options)
			if gone(failure) {
				if err != nil || len(videos) != 0 {
					t.Fatalf("disappeared node = %v, %v", videos, err)
				}
			} else if !errors.Is(err, failure) || !strings.Contains(err.Error(), "/dev/video0") {
				t.Fatalf("probe error = %v", err)
			}
		})
	}
	f := newTree(t)
	f.video("video0", "HDMI", "devices/platform/video", captureInfo())
	options := f.options()
	options.Probe = func(context.Context, string) (v4l2.Info, error) {
		if err := os.Remove(filepath.Join(f.root, "class/video4linux/video0")); err != nil {
			t.Fatal(err)
		}
		return captureInfo(), nil
	}
	if videos, err := DiscoverVideo(t.Context(), options); err != nil || len(videos) != 0 {
		t.Fatalf("disappeared after probe = %v, %v", videos, err)
	}
}

func TestAttributeAndClassFailures(t *testing.T) {
	for _, test := range []struct{ class, path string }{
		{"video", "devices/platform/video/video4linux/video0/name"},
		{"video", "devices/usb4/4-1/idVendor"},
		{"audio", "devices/platform/audio/sound/card0/id"},
		{"audio", "devices/usb4/4-1/idVendor"},
	} {
		t.Run(test.class+"/"+test.path, func(t *testing.T) {
			f := newTree(t)
			parent := "devices/platform/" + test.class
			if strings.Contains(test.path, "usb4") {
				parent = f.usb("devices/usb4/4-1", "2b89", "5389")
			}
			if test.class == "video" {
				f.video("video0", "HDMI", parent, captureInfo())
			} else {
				f.card("card0", "audio", parent, "pcmC0D0c")
			}
			if err := os.Remove(filepath.Join(f.root, test.path)); err != nil {
				t.Fatal(err)
			}
			f.dir(test.path)
			var err error
			if test.class == "video" {
				_, err = DiscoverVideo(t.Context(), f.options())
			} else {
				_, err = DiscoverAudio(t.Context(), f.root)
			}
			if !errors.Is(err, syscall.EISDIR) {
				t.Fatalf("attribute failure = %v", err)
			}
		})
	}
	for _, class := range []string{"tty", "video4linux", "sound"} {
		t.Run(class, func(t *testing.T) {
			f := newTree(t)
			if err := os.Remove(filepath.Join(f.root, "class", class)); err != nil {
				t.Fatal(err)
			}
			f.write("class/"+class, "not a directory")
			if _, err := Discover(t.Context(), f.options()); !errors.Is(err, syscall.ENOTDIR) {
				t.Fatalf("class failure = %v", err)
			}
		})
	}
}

func TestMissingAttributesAndEmptyName(t *testing.T) {
	f := newTree(t)
	f.video("video0", "", "devices/platform/video", captureInfo())
	f.dir("class/video4linux/video1") // disappeared name
	f.link("class/video4linux/video2", filepath.Join(f.root, "missing"))
	f.dir("class/sound/card0") // disappeared id
	f.link("class/sound/card1", filepath.Join(f.root, "missing"))
	inventory, err := Discover(t.Context(), f.options())
	if err != nil || len(inventory.Video) != 1 || inventory.Video[0].Name != "video0" || len(inventory.Audio) != 0 {
		t.Fatalf("missing/empty attributes = %+v, %v", inventory, err)
	}
}

type failedWriter struct{ failure error }

func (writer failedWriter) Write([]byte) (int, error) { return 0, writer.failure }

func TestWriteListGolden(t *testing.T) {
	for _, name := range []string{"empty", "attached", "ambiguous"} {
		t.Run(name, func(t *testing.T) {
			f := newTree(t)
			if name != "empty" {
				f = populatedTree(t)
			}
			if name == "ambiguous" {
				usb := f.usb("devices/usb4/4-2", "2b89", "5389")
				f.video("video10", "UGREEN 15389", usb+"/4-2:1.0", captureInfo())
				f.card("card10", "U15389", usb+"/4-2:1.2", "pcmC10D0c")
				ch341 := f.usb("devices/usb1/1-14", "1a86", "7522")
				f.tty("ttyUSB2", ch341)
			}
			inventory, err := Discover(t.Context(), f.options())
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := WriteList(&output, inventory); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", name+".golden.txt"))
			if err != nil {
				t.Fatalf("%v\nactual:\n%s", err, output.String())
			}
			if output.String() != string(want) {
				t.Fatalf("list mismatch\nactual:\n%swant:\n%s", output.String(), want)
			}
			if strings.Contains(output.String(), "ttyUSB1") || strings.Contains(output.String(), "metadata") {
				t.Fatal("unsupported tty or metadata node listed")
			}
		})
	}
	failure := errors.New("output failed")
	if err := WriteList(failedWriter{failure}, Inventory{Video: []VideoDevice{{Path: "/dev/video0", Name: "HDMI"}}}); !errors.Is(err, failure) {
		t.Fatalf("writer error lost: %v", err)
	}
}
