// Command discover maps ALSA cards to USB VID:PID and capture PCMs via sysfs/procfs.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type card struct {
	num      int
	id       string
	vidpid   string   // "" for non-USB cards
	captures []string // /dev/snd/pcmC{n}D{d}c
	rates    []int    // from stream0 Capture section (USB only)
	channels int
}

// usbVidPid walks up from a sysfs device dir to the first ancestor with idVendor/idProduct.
func usbVidPid(dir string) string {
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		v, e1 := os.ReadFile(filepath.Join(d, "idVendor"))
		p, e2 := os.ReadFile(filepath.Join(d, "idProduct"))
		if e1 == nil && e2 == nil {
			return strings.ToUpper(strings.TrimSpace(string(v)) + ":" + strings.TrimSpace(string(p)))
		}
	}
	return ""
}

// parseStream extracts capture rates and max channels from /proc/asound/cardN/streamM.
func parseStream(s string) (rates []int, channels int) {
	inCap := false
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case l == "Capture:":
			inCap = true
		case l == "Playback:":
			inCap = false
		case !inCap:
		case strings.HasPrefix(l, "Channels:"):
			if n, err := strconv.Atoi(strings.TrimSpace(l[9:])); err == nil {
				channels = max(channels, n)
			}
		case strings.HasPrefix(l, "Rates:"):
			body := strings.TrimSuffix(strings.TrimSpace(l[6:]), "(continuous)")
			for _, f := range strings.FieldsFunc(body, func(r rune) bool { return r == ',' || r == '-' || r == ' ' }) {
				if n, err := strconv.Atoi(f); err == nil {
					rates = append(rates, n)
				}
			}
		}
	}
	return
}

func scan() (cards []card) {
	dirs, _ := filepath.Glob("/sys/class/sound/card[0-9]*")
	for _, d := range dirs {
		var c card
		var err error
		if c.num, err = strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "card")); err != nil {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(d, "id"))
		c.id = strings.TrimSpace(string(b))
		if real, err := filepath.EvalSymlinks(filepath.Join(d, "device")); err == nil {
			c.vidpid = usbVidPid(real)
		}
		c.captures, _ = filepath.Glob(fmt.Sprintf("/dev/snd/pcmC%dD*c", c.num))
		streams, _ := filepath.Glob(fmt.Sprintf("/proc/asound/card%d/stream*", c.num))
		for _, s := range streams {
			if b, err := os.ReadFile(s); err == nil {
				r, ch := parseStream(string(b))
				c.rates, c.channels = append(c.rates, r...), max(c.channels, ch)
			}
		}
		cards = append(cards, c)
	}
	return
}

func main() {
	if len(os.Args) > 1 { // parse a stream file given as argument
		b, err := os.ReadFile(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		r, ch := parseStream(string(b))
		fmt.Printf("rates=%v channels=%d\n", r, ch)
		return
	}
	for _, c := range scan() {
		fmt.Printf("card%d id=%q vidpid=%q capture=%v rates=%v ch=%d\n", c.num, c.id, c.vidpid, c.captures, c.rates, c.channels)
	}
}
