//go:build linux && (amd64 || arm64)

package serial

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

type sysfsFixture struct {
	t    *testing.T
	root string
}

func newSysfsFixture(t *testing.T) sysfsFixture {
	t.Helper()
	f := sysfsFixture{t: t, root: t.TempDir()}
	f.dir("class/tty")
	f.dir("devices")
	return f
}

func (f sysfsFixture) dir(path string) string {
	f.t.Helper()
	path = filepath.Join(f.root, path)
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f sysfsFixture) write(path, value string) {
	f.t.Helper()
	path = filepath.Join(f.root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f sysfsFixture) usb(path, vid, pid string) string {
	f.t.Helper()
	f.write(path+"/idVendor", vid)
	f.write(path+"/idProduct", pid)
	return filepath.Join(f.root, path)
}

func (f sysfsFixture) tty(name, target string) {
	f.t.Helper()
	dir := f.dir("class/tty/" + name)
	if target != "" {
		if err := os.Symlink(target, filepath.Join(dir, "device")); err != nil {
			f.t.Fatal(err)
		}
	}
}

func TestFindUSBDevice(t *testing.T) {
	f := newSysfsFixture(t)
	f.usb("devices/usb2", "1d6b", "0002")
	device := f.usb("devices/usb2/2-1", " 1A86\n", "752B\n")
	video := f.dir("devices/usb2/2-1/2-1:1.0/video4linux/video0")
	audio := f.dir("devices/usb2/2-1/2-1:1.1/sound/card1")
	alias := filepath.Join(f.root, "video-device")
	if err := os.Symlink(video, alias); err != nil {
		t.Fatal(err)
	}
	want := USBDevice{VID: "1a86", PID: "752b", BusPath: device}
	for _, path := range []string{device, video, audio, alias} {
		got, err := FindUSBDevice(f.root, path)
		if err != nil || got != want {
			t.Fatalf("FindUSBDevice(%q) = %+v, %v; want %+v", path, got, err, want)
		}
	}
	other := f.usb("devices/usb2/2-2", "1a86", "752b")
	got, err := FindUSBDevice(f.root, other)
	if err != nil || got.BusPath == want.BusPath {
		t.Fatalf("identical devices were not distinguished: %+v, %v", got, err)
	}
}

func TestFindUSBDeviceRootSymlink(t *testing.T) {
	f := newSysfsFixture(t)
	device := f.usb("devices/usb1/1-1", "1a86", "7523")
	alias := filepath.Join(t.TempDir(), "sys")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	got, err := FindUSBDevice(alias, filepath.Join(alias, "devices/usb1/1-1"))
	if err != nil || got.BusPath != device {
		t.Fatalf("FindUSBDevice through sysfs alias = %+v, %v", got, err)
	}
}

func TestFindUSBDeviceNoAncestor(t *testing.T) {
	f := newSysfsFixture(t)
	platform := f.dir("devices/platform/serial8250/ttyS0")
	outside := f.usb("devices-other/usb1/1-1", "1a86", "7523")
	link := filepath.Join(f.dir("devices/platform"), "escaped")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{platform, filepath.Join(f.root, "devices"), outside, link} {
		if _, err := FindUSBDevice(f.root, path); !errors.Is(err, ErrNoUSB) {
			t.Errorf("FindUSBDevice(%q) = %v; want ErrNoUSB", path, err)
		}
	}
	if _, err := FindUSBDevice(f.root, filepath.Join(f.root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing device: %v", err)
	}
	if _, err := FindUSBDevice(filepath.Join(f.root, "missing"), platform); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing sysfs root: %v", err)
	}
}

func TestFindUSBDeviceMissingProduct(t *testing.T) {
	f := newSysfsFixture(t)
	parent := f.usb("devices/usb1/1-1", "1a86", "7523")
	f.write("devices/usb1/1-1/interface/idVendor", "incomplete")
	got, err := FindUSBDevice(f.root, filepath.Join(parent, "interface"))
	if err != nil || got.BusPath != parent {
		t.Fatalf("first complete USB ancestor = %+v, %v; want %q", got, err, parent)
	}
}

func TestFindUSBDeviceAttributeErrors(t *testing.T) {
	for _, name := range []string{"idVendor", "idProduct"} {
		t.Run(name, func(t *testing.T) {
			f := newSysfsFixture(t)
			f.write("devices/broken/idVendor", "1a86")
			if name == "idVendor" {
				if err := os.Remove(filepath.Join(f.root, "devices/broken/idVendor")); err != nil {
					t.Fatal(err)
				}
			}
			f.dir("devices/broken/" + name)
			if _, err := FindUSBDevice(f.root, filepath.Join(f.root, "devices/broken")); !errors.Is(err, syscall.EISDIR) {
				t.Fatalf("bad %s: %v; want EISDIR", name, err)
			}
		})
	}
}

func TestDiscover(t *testing.T) {
	f := newSysfsFixture(t)
	ch340 := f.usb("devices/usb1/1-1", "1A86\n", "7523\n")
	ch341 := f.usb("devices/usb1/1-2", "1a86", "7522")
	other := f.usb("devices/usb1/1-3", "0403", "6001")
	f.tty("ttyUSB10", ch341)
	f.tty("ttyUSB2", ch340)
	f.tty("ttyUSB0", other)
	f.tty("ttyUSB3", f.dir("devices/platform/serial8250/ttyS0"))
	f.tty("ttyUSB4", "")
	f.tty("ttyUSB5", filepath.Join(f.root, "does-not-exist"))
	f.tty("ttyUSB6", f.usb("devices-other/usb1/1-1", "1a86", "7523"))
	for _, name := range []string{"ttyS0", "ttyACM0", "ttyUSB", "ttyUSBx", "ttyUSB+1", "ttyUSB999999999999999999999999999999"} {
		f.tty(name, ch340)
	}
	got, err := Discover(f.root)
	want := []string{"/dev/ttyUSB2", "/dev/ttyUSB10"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Discover = %v, %v; want %v", got, err, want)
	}
	if path, err := Detect(f.root); path != "" || !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "/dev/ttyUSB2, /dev/ttyUSB10") {
		t.Fatalf("Detect multiple = %q, %v", path, err)
	}
}

func TestDetect(t *testing.T) {
	f := newSysfsFixture(t)
	if path, err := Detect(f.root); path != "" || !errors.Is(err, ErrNotFound) {
		t.Fatalf("Detect none = %q, %v", path, err)
	}
	if paths, err := Discover(f.root); err != nil || len(paths) != 0 {
		t.Fatalf("Discover none = %v, %v", paths, err)
	}
	device := f.usb("devices/usb1/1-1", "1a86", "7523")
	f.tty("ttyUSB0", device)
	if path, err := Detect(f.root); path != "/dev/ttyUSB0" || err != nil {
		t.Fatalf("Detect one = %q, %v", path, err)
	}
	if err := os.RemoveAll(filepath.Join(f.root, "class/tty/ttyUSB0")); err != nil {
		t.Fatal(err)
	}
	f.tty("ttyUSB1", device)
	if path, err := Detect(f.root); path != "/dev/ttyUSB1" || err != nil {
		t.Fatalf("Detect moved device = %q, %v", path, err)
	}
}

func TestDiscoverErrors(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing sysfs: %v", err)
	}
	if _, err := Detect(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing sysfs in Detect: %v", err)
	}
	f := newSysfsFixture(t)
	f.dir("devices/broken/idVendor")
	f.tty("ttyUSB0", filepath.Join(f.root, "devices/broken"))
	if _, err := Discover(f.root); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("attribute error in Discover: %v; want EISDIR", err)
	}
}
