//go:build darwin && (amd64 || arm64)

package serial

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DiscoveredDevice is a callout port with its USB ancestor's identity.
type DiscoveredDevice struct {
	Path string
	USB  USBDevice
}

// DiscoverDevices reads the IOService registry without opening serial ports.
// IDs come from USB ancestors, not driver-dependent cu.* filename guesses.
func DiscoverDevices(ctx context.Context) ([]DiscoveredDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// -l includes child serial properties; -c alone lists only USB properties.
	data, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-a", "-l", "-r", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return nil, fmt.Errorf("serial: read IORegistry: %w", errors.Join(ctx.Err(), err))
	}
	return parseRegistry(data)
}

// Discover returns supported CH340/CH341 callout ports in lexical order.
// sysRoot is used only on Linux and is ignored on macOS.
func Discover(sysRoot string) ([]string, error) {
	return discover(context.Background(), sysRoot)
}

func discover(ctx context.Context, sysRoot string) ([]string, error) {
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

func parseRegistry(data []byte) ([]DiscoveredDevice, error) {
	if len(bytes.TrimSpace(data)) == 0 { // ioreg emits nothing when no class matches.
		return nil, nil
	}
	var document struct {
		XMLName xml.Name        `xml:"plist"`
		Root    registryElement `xml:"array"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("serial: decode IORegistry: %w", err)
	}
	if document.Root.XMLName.Local != "array" {
		return nil, errors.New("serial: IORegistry property list has no root array")
	}
	seen := make(map[string]DiscoveredDevice)
	var walk func(registryElement, USBDevice)
	walk = func(node registryElement, ancestor USBDevice) {
		switch node.XMLName.Local {
		case "array":
			for _, child := range node.Children {
				walk(child, ancestor)
			}
		case "dict":
			// Interfaces and serial drivers can repeat vendor/product IDs. Only
			// a USB device descriptor/class starts a new shared USB identity.
			deviceDescriptor := node.property("bDeviceClass").XMLName.Local != ""
			interfaceDescriptor := node.property("bInterfaceNumber").XMLName.Local != ""
			class := node.property("IOObjectClass").Text
			isDevice := !interfaceDescriptor && (deviceDescriptor || class == "IOUSBHostDevice" || class == "IOUSBDevice")
			if vid, ok := node.number("idVendor"); ok && isDevice {
				pid, _ := node.number("idProduct")
				ancestor = USBDevice{VID: fmt.Sprintf("%04x", vid), PID: fmt.Sprintf("%04x", pid)}
				if id, ok := node.number("IORegistryEntryID"); ok {
					ancestor.BusPath = fmt.Sprintf("ioreg:%x", id)
				}
			}
			if ancestor.VID == "1a86" && (ancestor.PID == "7523" || ancestor.PID == "7522") {
				if path := node.property("IOCalloutDevice").Text; strings.HasPrefix(path, "/dev/cu.") {
					seen[path] = DiscoveredDevice{Path: path, USB: ancestor}
				}
			}
			walk(node.property("IORegistryEntryChildren"), ancestor)
		}
	}
	walk(document.Root, USBDevice{})
	devices := make([]DiscoveredDevice, 0, len(seen))
	for _, device := range seen {
		devices = append(devices, device)
	}
	slices.SortFunc(devices, func(a, b DiscoveredDevice) int { return strings.Compare(a.Path, b.Path) })
	return devices, nil
}

// encoding/xml owns XML decoding, including unrelated plist data properties.
// Only the dictionary entries needed for USB/serial discovery are interpreted.
type registryElement struct {
	XMLName  xml.Name
	Text     string            `xml:",chardata"`
	Children []registryElement `xml:",any"`
}

func (element registryElement) property(name string) registryElement {
	for i := 0; i+1 < len(element.Children); i += 2 {
		key := element.Children[i]
		if key.XMLName.Local == "key" && key.Text == name {
			return element.Children[i+1]
		}
	}
	return registryElement{}
}

func (element registryElement) number(name string) (uint64, bool) {
	value := element.property(name)
	if value.XMLName.Local != "integer" {
		return 0, false
	}
	number, err := strconv.ParseUint(strings.TrimSpace(value.Text), 0, 64)
	return number, err == nil
}
