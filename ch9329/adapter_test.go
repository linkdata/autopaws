package ch9329

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestAdapterPacketsAndModes(t *testing.T) {
	for _, test := range []struct {
		frame []byte
		hex   string
	}{
		{GetInfo(), "57ab00010003"},
		{GetParameters(), "57ab0008000a"},
		{Reset(), "57ab000f0011"},
	} {
		if hex.EncodeToString(test.frame) != test.hex {
			t.Fatalf("packet=%x; want %s", test.frame, test.hex)
		}
	}
	var parameters [ParameterSize]byte
	for index := range parameters {
		parameters[index] = byte(index)
	}
	set := SetParameters(parameters)
	if len(set) != 56 || set[3] != 9 || set[4] != ParameterSize || !bytes.Equal(set[5:55], parameters[:]) {
		t.Fatalf("SET lost parameter bytes: %x", set)
	}
	var sum byte
	for _, value := range set[:55] {
		sum += value
	}
	if set[55] != sum {
		t.Fatal("SET checksum mismatch")
	}
	for raw := range 256 {
		want := raw == 0 || raw == 2 || raw == 128 || raw == 130
		if ValidUSBMode(byte(raw)) != want {
			t.Fatalf("raw mode %d validation differs", raw)
		}
	}
	parameters[0] = 0x82
	mode, err := ParseUSBMode([]byte{0x38, 1, 0, 0, 0, 0, 0, 0}, parameters)
	if err != nil || mode != (USBMode{RawMode: 0x82, Mode: 2, Source: "hardware_pins", FirmwareVersion: 0x38, USBEnumerated: true}) {
		t.Fatalf("mode=%+v, %v", mode, err)
	}
	parameters[0] = 2
	if mode, err := ParseUSBMode(make([]byte, 8), parameters); err != nil || mode.Source != "software" || mode.USBEnumerated {
		t.Fatalf("software mode=%+v, %v", mode, err)
	}
	if _, err := ParseUSBMode(nil, parameters); err == nil {
		t.Fatal("short INFO accepted")
	}
	parameters[0] = 0x40
	if _, err := ParseUSBMode(make([]byte, 8), parameters); err == nil || errors.Is(err, ErrUSBMode) {
		t.Fatal("unrecognized configuration must fail as a device/protocol error")
	}
}
