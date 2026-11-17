//go:build linux && (amd64 || arm64)

// Package devices discovers supported serial ports, video nodes and capture PCM
// devices through Linux sysfs. Discovery never configures or streams hardware.
package devices

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/linkdata/autopaws/serial"
	"github.com/linkdata/autopaws/v4l2"
)

// ErrNoVideo means no node passed the supported capture-node filter.
var ErrNoVideo = errors.New("devices: no video capture device found")

// SerialDevice is one supported CH340/CH341 tty.
type SerialDevice struct {
	Path string
	USB  serial.USBDevice
}

// VideoDevice is one node with node-specific VIDEO_CAPTURE capability.
type VideoDevice struct {
	Path, Name string
	USB        serial.USBDevice
	Formats    []v4l2.PixelFormat
}

// AudioDevice is one capture PCM, with its descriptive ALSA card ID.
type AudioDevice struct {
	Path, CardID string
	Card, Device int
	USB          serial.USBDevice
}

// Inventory is numerically ordered within each device class. USB identities may
// be empty for non-USB devices; such identities never correlate video and audio.
type Inventory struct {
	Serial []SerialDevice
	Video  []VideoDevice
	Audio  []AudioDevice
}

// Options supplies the sysfs root and a read-only video probe. Empty/nil fields
// use /sys and v4l2.Probe. Tests can inject a temporary tree and fake probe.
type Options struct {
	SysRoot string
	Probe   func(context.Context, string) (v4l2.Info, error)
}

// Discover returns all three inventories without opening tty or PCM devices.
func Discover(ctx context.Context, options Options) (Inventory, error) {
	var inventory Inventory
	var err error
	if inventory.Serial, err = DiscoverSerial(ctx, options.SysRoot); err != nil {
		return Inventory{}, err
	}
	if inventory.Video, err = DiscoverVideo(ctx, options); err != nil {
		return Inventory{}, err
	}
	if inventory.Audio, err = DiscoverAudio(ctx, options.SysRoot); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

// DiscoverSerial reuses serial's CH340/CH341 filter and numeric ttyUSB ordering.
// Auto-selection remains serial.Detect: none and multiple matches are distinct.
func DiscoverSerial(ctx context.Context, sysRoot string) ([]SerialDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := rootPath(sysRoot)
	paths, err := serial.Discover(root)
	if err != nil {
		return nil, err
	}
	devices := make([]SerialDevice, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		usb, err := serial.FindUSBDevice(root, filepath.Join(root, "class", "tty", filepath.Base(path), "device"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		devices = append(devices, SerialDevice{Path: path, USB: usb})
	}
	return devices, ctx.Err()
}

// DiscoverVideo returns capture nodes and all advertised pixel formats. The
// presence of DEVICE_CAPS is required; aggregate sibling capture bits never make
// a metadata node eligible. MJPG/STREAMING support is checked later by Open.
func DiscoverVideo(ctx context.Context, options Options) ([]VideoDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := rootPath(options.SysRoot)
	probe := options.Probe
	if probe == nil {
		probe = v4l2.Probe
	}
	entries, err := readClass(root, "video4linux")
	if err != nil {
		return nil, err
	}
	devices := make([]VideoDevice, 0)
	for _, node := range nodes(entries, "video") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, "class", "video4linux", node.name)
		name, err := os.ReadFile(filepath.Join(dir, "name"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		path := "/dev/" + node.name
		info, err := probe(ctx, path)
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", path, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if info.Capabilities&v4l2.CapDeviceCaps == 0 || info.DeviceCaps&v4l2.CapVideoCapture == 0 {
			continue
		}
		usb, err := usbDevice(root, dir)
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		label := strings.TrimSpace(string(name))
		if label == "" {
			label = node.name
		}
		devices = append(devices, VideoDevice{Path: path, Name: label, USB: usb, Formats: slices.Clone(info.Formats)})
	}
	return devices, ctx.Err()
}

// DiscoverAudio lists capture PCM nodes, including non-USB cards. The card ID is
// descriptive only; USB BusPath, not card number/ID or VID:PID, correlates audio.
func DiscoverAudio(ctx context.Context, sysRoot string) ([]AudioDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := rootPath(sysRoot)
	entries, err := readClass(root, "sound")
	if err != nil {
		return nil, err
	}
	devices := make([]AudioDevice, 0)
	for _, card := range nodes(entries, "card") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, "class", "sound", card.name)
		id, err := os.ReadFile(filepath.Join(dir, "id"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		usb, err := usbDevice(root, dir)
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pcmEntries, err := os.ReadDir(dir)
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, pcm := range nodes(pcmEntries, "pcmC"+strings.TrimPrefix(card.name, "card")+"D") {
			devices = append(devices, AudioDevice{
				Path: "/dev/snd/" + pcm.name, CardID: strings.TrimSpace(string(id)),
				Card: card.index, Device: pcm.index, USB: usb,
			})
		}
	}
	return devices, ctx.Err()
}

type node struct {
	name  string
	index int
}

func nodes(entries []os.DirEntry, prefix string) []node {
	var matches []node
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok {
			continue
		}
		if strings.HasPrefix(prefix, "pcm") {
			suffix, ok = strings.CutSuffix(suffix, "c")
		}
		if !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		matches = append(matches, node{name: entry.Name(), index: index})
	}
	slices.SortFunc(matches, func(a, b node) int {
		if order := cmp.Compare(a.index, b.index); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	return matches
}

func rootPath(root string) string {
	if root == "" {
		return "/sys"
	}
	return root
}

func gone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENODEV)
}

func readClass(root, class string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(filepath.Join(root, "class", class))
	if errors.Is(err, fs.ErrNotExist) {
		// A missing optional subsystem is normal; an invalid root is not.
		if _, rootErr := os.Stat(root); rootErr != nil {
			return nil, rootErr
		}
		return nil, nil
	}
	return entries, err
}

func usbDevice(root, path string) (serial.USBDevice, error) {
	usb, err := serial.FindUSBDevice(root, path)
	if errors.Is(err, serial.ErrNoUSB) {
		return serial.USBDevice{}, nil
	}
	return usb, err
}

// SelectVideo chooses the first non-webcam name in numeric discovery order, or
// the first capture node when every name contains a webcam keyword.
func SelectVideo(videos []VideoDevice) (VideoDevice, error) {
	for _, video := range videos {
		name := strings.ToLower(video.Name)
		webcam := false
		for _, keyword := range []string{"webcam", "camera", "ir camera", "front", "rear", "facetime"} {
			if strings.Contains(name, keyword) {
				webcam = true
				break
			}
		}
		if !webcam {
			return video, nil
		}
	}
	if len(videos) == 0 {
		return VideoDevice{}, ErrNoVideo
	}
	return videos[0], nil
}

// AudioForVideo selects the lowest card/device capture PCM with the same
// nonempty USB ancestor as video. Identical VID:PID/card IDs do not cross-pair.
func AudioForVideo(video VideoDevice, audios []AudioDevice) (AudioDevice, bool) {
	var selected AudioDevice
	found := false
	for _, audio := range audios {
		if video.USB.BusPath == "" || audio.USB.BusPath != video.USB.BusPath {
			continue
		}
		if !found || audio.Card < selected.Card || audio.Card == selected.Card && audio.Device < selected.Device {
			selected, found = audio, true
		}
	}
	return selected, found
}
