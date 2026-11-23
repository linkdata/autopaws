package monotime

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                             = windows.NewLazySystemDLL("kernel32.dll")
	queryPerformanceCounter              = kernel32.NewProc("QueryPerformanceCounter")
	queryPerformanceFrequency            = kernel32.NewProc("QueryPerformanceFrequency")
	performanceFrequency, frequencyError = readPerformanceFrequency()
)

func readPerformanceFrequency() (int64, error) {
	var frequency int64
	if ok, _, err := queryPerformanceFrequency.Call(uintptr(unsafe.Pointer(&frequency))); ok == 0 {
		return 0, fmt.Errorf("QueryPerformanceFrequency: %w", err)
	}
	if frequency <= 0 {
		return 0, errors.New("invalid performance counter frequency")
	}
	return frequency, nil
}

// Now returns nanoseconds on the Windows performance counter clock, shared
// across media sources independently of process startup and wall-clock changes.
func Now() (int64, error) {
	if frequencyError != nil {
		return 0, frequencyError
	}
	var counter int64
	if ok, _, err := queryPerformanceCounter.Call(uintptr(unsafe.Pointer(&counter))); ok == 0 {
		return 0, fmt.Errorf("QueryPerformanceCounter: %w", err)
	}
	return counterNanoseconds(counter, performanceFrequency)
}

func counterNanoseconds(counter, frequency int64) (int64, error) {
	if counter < 0 || frequency <= 0 {
		return 0, errors.New("invalid performance counter timestamp")
	}
	seconds := counter / frequency
	hi, lo := bits.Mul64(uint64(counter%frequency), 1e9)
	fraction, _ := bits.Div64(hi, lo, uint64(frequency))
	if seconds > (math.MaxInt64-int64(fraction))/1e9 {
		return 0, errors.New("performance counter timestamp overflows nanoseconds")
	}
	return seconds*1e9 + int64(fraction), nil
}
