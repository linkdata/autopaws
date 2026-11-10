//go:build linux && (amd64 || arm64)

package v4l2

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unsafe"
)

// Info describes a V4L2 node found in sysfs.
type Info struct {
	Path    string // /dev/videoN
	Name    string // sysfs name (card name)
	Index   int    // sysfs index: 0 = primary node, UVC metadata node is 1
	VIDPID  string // "VVVV:PPPP" uppercase, empty if not USB
	Capture bool   // QUERYCAP device_caps has VIDEO_CAPTURE (false for META nodes)
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// List enumerates /sys/class/video4linux in numeric order. Capture is
// determined by opening each node and issuing QUERYCAP; nodes that cannot
// be opened report Capture=false.
func List() (out []Info) {
	dirs, _ := filepath.Glob("/sys/class/video4linux/video*")
	num := func(s string) int { n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(s), "video")); return n }
	slices.SortFunc(dirs, func(a, b string) int { return num(a) - num(b) })
	for _, dir := range dirs {
		in := Info{Path: "/dev/" + filepath.Base(dir), Name: readTrim(dir + "/name")}
		in.Index, _ = strconv.Atoi(readTrim(dir + "/index"))
		if real, err := filepath.EvalSymlinks(dir + "/device"); err == nil {
			for p := real; p != "/" && p != "."; p = filepath.Dir(p) {
				if v := readTrim(p + "/idVendor"); v != "" {
					in.VIDPID = strings.ToUpper(v + ":" + readTrim(p+"/idProduct"))
					break
				}
			}
		}
		in.Capture = isCapture(in.Path)
		out = append(out, in)
	}
	return
}

func isCapture(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	var c capability
	var ierr error
	_ = rc.Control(func(fd uintptr) { ierr = ioctl(fd, vidiocQuerycap, unsafe.Pointer(&c)) })
	caps := c.Capabilities
	if caps&capDeviceCaps != 0 {
		caps = c.DeviceCaps
	}
	return ierr == nil && caps&capVideoCapture != 0
}
