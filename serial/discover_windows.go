//go:build windows && (amd64 || arm64)

package serial

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// DiscoveredDevice is a COM port with its USB Plug and Play identity.
type DiscoveredDevice struct {
	Path string
	USB  USBDevice
}

type windowsPort struct {
	name, instance string
	hardwareIDs    []string
}

var windowsUSBID = regexp.MustCompile(`(?i)^USB\\VID_([0-9a-f]{4})&PID_([0-9a-f]{4})(?:&|\\|$)`)

func supportedWindowsUSB(ids []string) (USBDevice, bool) {
	for _, id := range ids {
		match := windowsUSBID.FindStringSubmatch(id)
		if len(match) == 3 && strings.EqualFold(match[1], "1a86") &&
			(strings.EqualFold(match[2], "7523") || strings.EqualFold(match[2], "7522")) {
			return USBDevice{VID: strings.ToLower(match[1]), PID: strings.ToLower(match[2])}, true
		}
	}
	return USBDevice{}, false
}

// DiscoverDevices enumerates present Ports-class devices without opening them.
// SetupAPI also finds adapters omitted by the Win32_SerialPort WMI class.
func DiscoverDevices(ctx context.Context) ([]DiscoveredDevice, error) {
	return discoverWindowsDevices(ctx, readWindowsPorts)
}

func discoverWindowsDevices(ctx context.Context, read func(context.Context) ([]windowsPort, error)) ([]DiscoveredDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ports, err := read(ctx)
	if err != nil {
		return nil, errors.Join(ctx.Err(), err)
	}
	devices := make([]DiscoveredDevice, 0, len(ports))
	seen := make(map[string]bool)
	for _, port := range ports {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		usb, supported := supportedWindowsUSB(port.hardwareIDs)
		name, err := comName(port.name)
		if !supported || err != nil || port.instance == "" || seen[name] {
			continue
		}
		usb.BusPath = port.instance
		devices = append(devices, DiscoveredDevice{Path: name, USB: usb})
		seen[name] = true
	}
	slices.SortFunc(devices, func(left, right DiscoveredDevice) int {
		leftNumber, _ := strconv.ParseUint(left.Path[3:], 10, 32)
		rightNumber, _ := strconv.ParseUint(right.Path[3:], 10, 32)
		return cmp.Compare(leftNumber, rightNumber)
	})
	return devices, nil
}

func readWindowsPorts(ctx context.Context) (ports []windowsPort, err error) {
	class := windows.GUID{Data1: 0x4d36e978, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}
	set, err := windows.SetupDiGetClassDevsEx(&class, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, fmt.Errorf("serial: enumerate Windows ports: %w", err)
	}
	defer func() { err = errors.Join(err, set.Close()) }()
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := set.EnumDeviceInfo(index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return ports, nil
		}
		if err != nil {
			return nil, err
		}
		value, err := set.DeviceRegistryProperty(data, windows.SPDRP_HARDWAREID)
		if missingWindowsPort(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ids, ok := value.([]string)
		if !ok {
			return nil, errors.New("serial: Windows hardware IDs are not a string list")
		}
		if _, supported := supportedWindowsUSB(ids); !supported {
			continue
		}
		instance, err := set.DeviceInstanceID(data)
		if missingWindowsPort(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		keyHandle, err := set.OpenDevRegKey(data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_QUERY_VALUE)
		if missingWindowsPort(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		key := registry.Key(keyHandle)
		name, _, readErr := key.GetStringValue("PortName")
		closeErr := key.Close()
		if closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if missingWindowsPort(readErr) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		ports = append(ports, windowsPort{name: name, instance: instance, hardwareIDs: ids})
	}
}

func missingWindowsPort(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_INVALID_DATA) ||
		errors.Is(err, windows.ERROR_NO_SUCH_DEVINST) || errors.Is(err, windows.ERROR_DEVICE_NOT_CONNECTED)
}

// Discover lists supported COM names in numeric order. sysRoot is ignored.
func Discover(_ string) ([]string, error) {
	return discover(context.Background(), "")
}

func discover(ctx context.Context, _ string) ([]string, error) {
	devices, err := DiscoverDevices(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(devices))
	for i, device := range devices {
		paths[i] = device.Path
	}
	return paths, nil
}
