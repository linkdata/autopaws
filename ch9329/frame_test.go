package ch9329_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/linkdata/autopaws/ch9329"
)

type goldenFrames struct {
	Keyboard []struct {
		Name        string `json:"name"`
		Modifier    byte   `json:"modifier"`
		Keycodes    []byte `json:"keycodes"`
		ExpectedHex string `json:"expected_hex"`
	} `json:"keyboard"`
	Mouse []struct {
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		Args        [4]int `json:"args"`
		ExpectedHex string `json:"expected_hex"`
	} `json:"mouse"`
	ReleaseAllHex [2]string `json:"release_all_hex"`
}

func readGolden(t *testing.T) goldenFrames {
	t.Helper()
	data, err := os.ReadFile("testdata/frames.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenFrames
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func assertGolden(t *testing.T, got []byte, wantHex string) {
	t.Helper()
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = % x; want % x", got, want)
	}
}

func TestKeyboardGolden(t *testing.T) {
	for _, vector := range readGolden(t).Keyboard {
		t.Run(vector.Name, func(t *testing.T) {
			assertGolden(t, ch9329.Keyboard(vector.Modifier, vector.Keycodes...), vector.ExpectedHex)
		})
	}
}

func TestMouseGolden(t *testing.T) {
	for _, vector := range readGolden(t).Mouse {
		t.Run(vector.Kind+"/"+vector.Name, func(t *testing.T) {
			args := vector.Args
			var got []byte
			switch vector.Kind {
			case "absolute":
				got = ch9329.MouseAbsolute(byte(args[0]), args[1], args[2], args[3])
			case "relative":
				got = ch9329.MouseRelative(byte(args[0]), args[1], args[2], args[3])
			default:
				t.Fatalf("unknown mouse vector kind %q", vector.Kind)
			}
			assertGolden(t, got, vector.ExpectedHex)
		})
	}
}

func TestReleaseAllGolden(t *testing.T) {
	want := readGolden(t).ReleaseAllHex
	for i, got := range ch9329.ReleaseAll() {
		assertGolden(t, got, want[i])
	}
}

func TestFramesOwnTheirBytes(t *testing.T) {
	encoders := map[string]func() []byte{
		"keyboard": func() []byte { return ch9329.Keyboard(2, 4) },
		"absolute": func() []byte { return ch9329.MouseAbsolute(1, 1234, 2345, 1) },
		"relative": func() []byte { return ch9329.MouseRelative(2, -3, 4, -1) },
	}
	for name, encode := range encoders {
		t.Run(name, func(t *testing.T) {
			first := encode()
			second := encode()
			want := bytes.Clone(second)
			clear(first)
			if !bytes.Equal(second, want) {
				t.Fatalf("mutating one frame changed another result: % x; want % x", second, want)
			}
		})
	}

	keys := []byte{0, 4, 5}
	frame := ch9329.Keyboard(0, keys...)
	if !bytes.Equal(keys, []byte{0, 4, 5}) {
		t.Fatalf("Keyboard changed its input: % x", keys)
	}
	clear(keys)
	assertGolden(t, frame, "57ab000208000004050000000015")

	releases := ch9329.ReleaseAll()
	otherReleases := ch9329.ReleaseAll()
	clear(releases[0])
	assertGolden(t, releases[1], "57ab00050501000000000d")
	clear(releases[1])
	wantReleases := readGolden(t).ReleaseAllHex
	for i, got := range otherReleases {
		assertGolden(t, got, wantReleases[i])
	}
}

func FuzzFrames(f *testing.F) {
	f.Add(byte(0), []byte{}, byte(0), 0, 0, 0)
	f.Add(byte(255), []byte{0, 4, 0, 5, 6, 7, 8, 9, 10}, byte(7), 4095, -127, 127)
	f.Add(byte(2), []byte{255, 255, 255, 255, 255, 255}, byte(255), math.MinInt, math.MaxInt, math.MinInt)
	f.Fuzz(func(t *testing.T, modifier byte, keys []byte, buttons byte, x, y, wheel int) {
		keyboard := ch9329.Keyboard(modifier, keys...)
		assertFrame(t, keyboard, 0x02, 8)
		if keyboard[5] != modifier || keyboard[6] != 0 {
			t.Fatalf("invalid keyboard modifier/reserved byte: % x", keyboard)
		}

		absolute := ch9329.MouseAbsolute(buttons, x, y, wheel)
		assertFrame(t, absolute, 0x04, 7)
		if absolute[5] != 0x02 || absolute[6] != buttons ||
			binary.LittleEndian.Uint16(absolute[7:9]) > 4095 ||
			binary.LittleEndian.Uint16(absolute[9:11]) > 4095 || absolute[11] == 0x80 {
			t.Fatalf("invalid absolute mouse payload: % x", absolute)
		}

		relative := ch9329.MouseRelative(buttons, x, y, wheel)
		assertFrame(t, relative, 0x05, 5)
		if relative[5] != 0x01 || relative[6] != buttons ||
			relative[7] == 0x80 || relative[8] == 0x80 || relative[9] == 0x80 {
			t.Fatalf("invalid relative mouse payload: % x", relative)
		}
	})
}

func assertFrame(t *testing.T, frame []byte, command byte, payloadLength int) {
	t.Helper()
	if len(frame) != payloadLength+6 {
		t.Fatalf("frame length = %d; want %d", len(frame), payloadLength+6)
	}
	if !bytes.Equal(frame[:3], []byte{0x57, 0xab, 0x00}) ||
		frame[3] != command || int(frame[4]) != payloadLength {
		t.Fatalf("invalid frame header: % x", frame)
	}
	sum := 0
	for _, b := range frame[:len(frame)-1] {
		sum += int(b)
	}
	if frame[len(frame)-1] != byte(sum%256) {
		t.Fatalf("invalid checksum: % x", frame)
	}
}
