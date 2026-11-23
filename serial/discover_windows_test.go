//go:build windows && (amd64 || arm64)

package serial

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestWindowsDiscoveryFiltersIdentifiesAndSorts(t *testing.T) {
	ports := []windowsPort{
		{name: "COM12", instance: "usb-b", hardwareIDs: []string{`USB\VID_1A86&PID_7522&REV_0100`}},
		{name: "com3", instance: "usb-a", hardwareIDs: []string{`USB\VID_1A86&PID_7523`}},
		{name: "COM3", instance: "duplicate", hardwareIDs: []string{`USB\VID_1A86&PID_7523`}},
		{name: "COM2", instance: "unsupported", hardwareIDs: []string{`USB\VID_1234&PID_7523`}},
		{name: "COM4", instance: "prefix", hardwareIDs: []string{`USB\VID_1A86&PID_75230`}},
		{name: "COM5", instance: "not-usb", hardwareIDs: []string{`PCI\VID_1A86&PID_7523`}},
		{name: "LPT1", instance: "not-com", hardwareIDs: []string{`USB\VID_1A86&PID_7523`}},
		{name: "COM6", hardwareIDs: []string{`USB\VID_1A86&PID_7523`}},
	}
	got, err := discoverWindowsDevices(t.Context(), func(context.Context) ([]windowsPort, error) { return ports, nil })
	want := []DiscoveredDevice{
		{Path: "COM3", USB: USBDevice{VID: "1a86", PID: "7523", BusPath: "usb-a"}},
		{Path: "COM12", USB: USBDevice{VID: "1a86", PID: "7522", BusPath: "usb-b"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("discovery = %+v, %v; want %+v", got, err, want)
	}
}

func TestWindowsDiscoveryPreservesErrorsAndCancellation(t *testing.T) {
	failure := errors.New("SetupAPI failure")
	if got, err := discoverWindowsDevices(t.Context(), func(context.Context) ([]windowsPort, error) { return nil, failure }); got != nil || !errors.Is(err, failure) {
		t.Fatalf("enumeration error = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := discoverWindowsDevices(ctx, func(context.Context) ([]windowsPort, error) {
		t.Fatal("enumerated after cancellation")
		return nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery = %v", err)
	}
}
