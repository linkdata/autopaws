//go:build (linux || darwin || windows) && (amd64 || arm64)

package devices

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/linkdata/autopaws/serial"
)

// WriteList writes human-readable inventory and automatic-selection markers.
// No devices or an ambiguous serial default are successful inventory results.
func WriteList(output io.Writer, inventory Inventory) error {
	video, _ := SelectVideo(inventory.Video)
	audio, _ := AudioForVideo(video, inventory.Audio)
	var text strings.Builder
	text.WriteString("* = auto-selected\n\nSerial ports (CH340/CH341):\n")
	for _, device := range inventory.Serial {
		fmt.Fprintf(&text, "  %s %s%s\n", marker(len(inventory.Serial) == 1), device.Path, usbLabel(device.USB))
	}
	if len(inventory.Serial) == 0 {
		text.WriteString("  (none)\n")
	} else if len(inventory.Serial) > 1 {
		text.WriteString("  Auto-detection is ambiguous; use -serial-device.\n")
	}
	text.WriteString("\nVideo capture devices:\n")
	for _, device := range inventory.Video {
		fmt.Fprintf(&text, "  %s %s: %s%s\n", marker(device.Path == video.Path), device.Path, device.Name, usbLabel(device.USB))
		text.WriteString("      Formats:")
		for i, format := range device.Formats {
			if i != 0 {
				text.WriteByte(',')
			}
			fmt.Fprintf(&text, " %s (%s)", formatFourCC(format.FourCC), format.Description)
		}
		if len(device.Formats) == 0 {
			text.WriteString(" (none)")
		}
		text.WriteByte('\n')
	}
	if len(inventory.Video) == 0 {
		text.WriteString("  (none)\n")
	}
	text.WriteString("\nAudio capture devices:\n")
	for _, device := range inventory.Audio {
		fmt.Fprintf(&text, "  %s %s: %s%s\n", marker(device.Path == audio.Path), device.Path, device.CardID, usbLabel(device.USB))
	}
	if len(inventory.Audio) == 0 {
		text.WriteString("  (none)\n")
	}
	_, err := io.WriteString(output, text.String())
	return err
}

func marker(selected bool) string {
	if selected {
		return "*"
	}
	return " "
}

func formatFourCC(value uint32) string {
	encoded := [4]byte{byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)}
	for _, char := range encoded {
		if char < 0x20 || char > 0x7e {
			return fmt.Sprintf("0x%08X", value)
		}
	}
	return string(encoded[:])
}

func usbLabel(device serial.USBDevice) string {
	if device.BusPath == "" {
		return ""
	}
	return fmt.Sprintf(" [USB %s:%s, port %s]", device.VID, device.PID, filepath.Base(device.BusPath))
}
