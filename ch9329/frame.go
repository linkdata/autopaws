// Package ch9329 encodes CH9329 keyboard and mouse frames without performing I/O.
package ch9329

// Keyboard returns a keyboard report with the supplied modifier bitmask and
// first six nonzero keycodes. Key order and duplicates are preserved.
func Keyboard(modifier byte, keycodes ...byte) []byte {
	var data [8]byte
	data[0] = modifier
	next := 2
	for _, key := range keycodes {
		if key == 0 {
			continue
		}
		data[next] = key
		next++
		if next == len(data) {
			break
		}
	}
	return frame(0x02, data[:]...)
}

// MouseAbsolute returns an absolute mouse report. Coordinates are clamped to
// 0–4095 and wheel movement to -127–127. The button bitmask is passed unchanged.
func MouseAbsolute(buttons byte, x, y, wheel int) []byte {
	x = min(max(x, 0), 4095)
	y = min(max(y, 0), 4095)
	wheel = min(max(wheel, -127), 127)
	return frame(0x04, 0x02, buttons, byte(x), byte(x>>8), byte(y), byte(y>>8), byte(wheel))
}

// MouseRelative returns a relative mouse report. Movement on each axis and
// wheel movement are clamped to -127–127. The button bitmask is passed unchanged.
func MouseRelative(buttons byte, dx, dy, wheel int) []byte {
	dx = min(max(dx, -127), 127)
	dy = min(max(dy, -127), 127)
	wheel = min(max(wheel, -127), 127)
	return frame(0x05, 0x01, buttons, byte(dx), byte(dy), byte(wheel))
}

// ReleaseAll returns a keyboard release followed by a relative mouse release.
// Every returned frame owns its bytes, as do the individual encoder results.
func ReleaseAll() [2][]byte {
	return [2][]byte{Keyboard(0), MouseRelative(0, 0, 0, 0)}
}

func frame(command byte, data ...byte) []byte {
	packet := make([]byte, 0, len(data)+6)
	packet = append(packet, 0x57, 0xab, 0x00, command, byte(len(data)))
	packet = append(packet, data...)
	var checksum byte
	for _, b := range packet {
		checksum += b
	}
	return append(packet, checksum)
}
