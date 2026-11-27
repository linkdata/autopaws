package ch9329

import (
	"errors"
)

const ParameterSize = 50

var ErrUSBMode = errors.New("ch9329: unsupported USB working mode")

// USBMode describes the configuration read from a CH9329, not its UART mode.
type USBMode struct {
	RawMode         byte   `json:"raw_mode"`
	Mode            byte   `json:"mode"`
	Source          string `json:"source"`
	FirmwareVersion byte   `json:"firmware_version_byte"`
	USBEnumerated   bool   `json:"usb_enumerated"`
}

// ValidUSBMode accepts software/pin selection while retaining keyboard+mouse.
func ValidUSBMode(raw byte) bool {
	return raw == 0 || raw == 2 || raw == 0x80 || raw == 0x82
}

func GetInfo() []byte       { return frame(0x01) }
func GetParameters() []byte { return frame(0x08) }
func Reset() []byte         { return frame(0x0f) }

func SetParameters(parameters [ParameterSize]byte) []byte {
	return frame(0x09, parameters[:]...)
}

func ParseUSBMode(info []byte, parameters [ParameterSize]byte) (USBMode, error) {
	if len(info) != 8 || info[1] > 1 || parameters[0]&^byte(0x83) != 0 {
		return USBMode{}, errors.New("ch9329: unrecognized adapter information/configuration")
	}
	source := "software"
	if parameters[0]&0x80 != 0 {
		source = "hardware_pins"
	}
	return USBMode{
		RawMode: parameters[0], Mode: parameters[0] & 3, Source: source,
		FirmwareVersion: info[0], USBEnumerated: info[1] == 1,
	}, nil
}
