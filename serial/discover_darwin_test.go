//go:build darwin && (amd64 || arm64)

package serial

import (
	"fmt"
	"reflect"
	"testing"
)

func registryFixture(body string) []byte {
	return []byte(`<?xml version="1.0"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><array>` + body + `</array></plist>`)
}

func usbFixture(vid, pid, id int, body string) string {
	return fmt.Sprintf(`<dict><key>IOObjectClass</key><string>IOUSBHostDevice</string><key>bDeviceClass</key><integer>0</integer><key>idVendor</key><integer>%d</integer><key>idProduct</key><integer>%d</integer><key>IORegistryEntryID</key><integer>%d</integer><key>IORegistryEntryChildren</key><array>%s</array></dict>`, vid, pid, id, body)
}

func calloutFixture(path string) string {
	return `<dict><key>IOCalloutDevice</key><string>` + path + `</string><key>IODialinDevice</key><string>/dev/tty.ignored</string></dict>`
}

func TestDarwinRegistryUSBAncestryAndCalloutSelection(t *testing.T) {
	first := usbFixture(0x1a86, 0x7523, 100, `<dict><key>IORegistryEntryChildren</key><array>`+calloutFixture("/dev/cu.usbserial-b")+`</array><key>unused</key><data>AA==</data><key>signed</key><integer>-1</integer></dict>`)
	// Preserve registry IDs larger than the exact integer range of float64.
	second := usbFixture(0x1a86, 0x7522, 0x20000000000001, calloutFixture("/dev/cu.usbserial-a"))
	decoy := usbFixture(0x1234, 0x7523, 102, calloutFixture("/dev/cu.wchusbserial-decoy"))
	// A supported outer ancestor must not turn a nested unrelated USB device
	// into a match. Duplicate IORegistry roots must not duplicate ports.
	nested := usbFixture(0x1a86, 0x7523, 103, decoy)
	got, err := parseRegistry(registryFixture(first + second + decoy + nested + first + calloutFixture("/dev/cu.no-usb")))
	want := []DiscoveredDevice{
		{Path: "/dev/cu.usbserial-a", USB: USBDevice{VID: "1a86", PID: "7522", BusPath: "ioreg:20000000000001"}},
		{Path: "/dev/cu.usbserial-b", USB: USBDevice{VID: "1a86", PID: "7523", BusPath: "ioreg:64"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("registry = %+v, %v; want %+v", got, err, want)
	}
}

func TestDarwinRegistryInterfacesRetainDeviceIdentity(t *testing.T) {
	interfaceNode := func(id, number int, path string) string {
		driver := `<dict><key>IOObjectClass</key><string>AppleUSBCHCOM</string><key>idVendor</key><integer>6790</integer><key>idProduct</key><integer>29987</integer><key>IORegistryEntryID</key><integer>300</integer><key>IORegistryEntryChildren</key><array>` + calloutFixture(path) + `</array></dict>`
		return fmt.Sprintf(`<dict><key>IOObjectClass</key><string>IOUSBHostInterface</string><key>idVendor</key><integer>6790</integer><key>idProduct</key><integer>29987</integer><key>IORegistryEntryID</key><integer>%d</integer><key>bInterfaceNumber</key><integer>%d</integer><key>IORegistryEntryChildren</key><array>%s</array></dict>`, id, number, driver)
	}
	first := usbFixture(0x1a86, 0x7523, 100, interfaceNode(200, 0, "/dev/cu.first"))
	// A nested device with identical VID/PID must still replace the identity;
	// interface and driver nodes repeating those IDs must retain it.
	nested := usbFixture(0x1a86, 0x7523, 101, interfaceNode(201, 1, "/dev/cu.nested"))
	outer := usbFixture(0x1a86, 0x7523, 102, nested)
	got, err := parseRegistry(registryFixture(first + outer))
	want := []DiscoveredDevice{
		{Path: "/dev/cu.first", USB: USBDevice{VID: "1a86", PID: "7523", BusPath: "ioreg:64"}},
		{Path: "/dev/cu.nested", USB: USBDevice{VID: "1a86", PID: "7523", BusPath: "ioreg:65"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("registry = %+v, %v; want %+v", got, err, want)
	}
}

// This is the connected CH340's class structure, with device paths/IDs sanitized.
// Without ioreg -l, child nodes keep their class/ID but lose serial properties.
func TestDarwinRegistryDriverKitSerialProperties(t *testing.T) {
	serial := `<dict><key>IOObjectClass</key><string>IOSerialBSDClient</string><key>IORegistryEntryID</key><integer>104</integer>%s</dict>`
	driver := `<dict><key>IOObjectClass</key><string>IOUserSerial</string><key>IORegistryEntryName</key><string>AppleUSBCHCOM</string><key>IORegistryEntryChildren</key><array>` + serial + `</array></dict>`
	iface := `<dict><key>IOObjectClass</key><string>IOUSBHostInterface</string><key>bInterfaceNumber</key><integer>0</integer><key>IORegistryEntryChildren</key><array>` + driver + `</array></dict>`
	for _, fullProperties := range []bool{false, true} {
		properties := ""
		var want []DiscoveredDevice
		if fullProperties {
			properties = `<key>IOCalloutDevice</key><string>/dev/cu.usbserial-test</string><key>IODialinDevice</key><string>/dev/tty.usbserial-test</string>`
			want = []DiscoveredDevice{{Path: "/dev/cu.usbserial-test", USB: USBDevice{VID: "1a86", PID: "7523", BusPath: "ioreg:64"}}}
		}
		data := registryFixture(usbFixture(0x1a86, 0x7523, 100, fmt.Sprintf(iface, properties)))
		got, err := parseRegistry(data)
		if err != nil || len(got) != len(want) || len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Fatalf("fullProperties=%t: got %+v, %v; want %+v", fullProperties, got, err, want)
		}
	}
}

func TestDarwinRegistryEmptyAndMalformed(t *testing.T) {
	for _, fixture := range [][]byte{nil, registryFixture("")} {
		if got, err := parseRegistry(fixture); err != nil || len(got) != 0 {
			t.Fatalf("empty = %+v, %v", got, err)
		}
	}
	for _, fixture := range []string{
		`<plist><array><dict><key>idVendor</key></array></plist>`,
		`<plist><array>`, `<not-plist/>`, `<plist><dict/></plist>`,
	} {
		if got, err := parseRegistry([]byte(fixture)); err == nil {
			t.Fatalf("malformed %q returned %+v", fixture, got)
		}
	}
}
