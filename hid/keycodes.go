// Package hid provides keyboard layouts and HID keycode lookup.
package hid

import (
	"strconv"
	"strings"
)

var modifiers = map[string]byte{
	"ctrl": 0x01, "lctrl": 0x01, "rctrl": 0x10,
	"shift": 0x02, "lshift": 0x02, "rshift": 0x20,
	"alt": 0x04, "lalt": 0x04, "ralt": 0x40,
	"win": 0x08, "lwin": 0x08, "rwin": 0x80,
	"gui": 0x08, "super": 0x08, "meta": 0x08,
}

var specialKeys = map[string]byte{
	"enter": 0x28, "return": 0x28, "escape": 0x29, "esc": 0x29,
	"backspace": 0x2a, "tab": 0x2b, "space": 0x2c, "capslock": 0x39,
	"f1": 0x3a, "f2": 0x3b, "f3": 0x3c, "f4": 0x3d,
	"f5": 0x3e, "f6": 0x3f, "f7": 0x40, "f8": 0x41,
	"f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"printscreen": 0x46, "scrolllock": 0x47, "pause": 0x48,
	"insert": 0x49, "home": 0x4a, "pageup": 0x4b, "delete": 0x4c,
	"end": 0x4d, "pagedown": 0x4e, "right": 0x4f, "left": 0x50,
	"down": 0x51, "up": 0x52, "numlock": 0x53,
}

// Modifier resolves a case-insensitive modifier name to its bitmask.
func Modifier(name string) (byte, bool) {
	bit, ok := modifiers[strings.ToLower(name)]
	return bit, ok
}

// SpecialKey resolves a case-insensitive named key or a hexadecimal keycode.
// Hexadecimal codes use 0x followed by digits and must fit in one byte.
// A zero keycode is valid; the bool distinguishes it from an invalid name.
func SpecialKey(name string) (byte, bool) {
	name = strings.ToLower(name)
	if keycode, ok := specialKeys[name]; ok {
		return keycode, true
	}
	return hexKeycode(name)
}

func hexKeycode(text string) (byte, bool) {
	if len(text) <= 2 || !strings.EqualFold(text[:2], "0x") {
		return 0, false
	}
	value, err := strconv.ParseUint(text[2:], 16, 8)
	return byte(value), err == nil
}
