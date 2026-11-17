//go:build linux && (amd64 || arm64)

package alsa

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

func TestABIFromLinuxHeaders(t *testing.T) {
	actual := map[string]uintptr{
		"mask.size":                     unsafe.Sizeof(mask{}),
		"mask.align":                    unsafe.Alignof(mask{}),
		"mask.Bits":                     unsafe.Offsetof(mask{}.Bits),
		"interval.size":                 unsafe.Sizeof(interval{}),
		"interval.align":                unsafe.Alignof(interval{}),
		"interval.Min":                  unsafe.Offsetof(interval{}.Min),
		"interval.Max":                  unsafe.Offsetof(interval{}.Max),
		"hwParams.size":                 unsafe.Sizeof(hwParams{}),
		"hwParams.align":                unsafe.Alignof(hwParams{}),
		"hwParams.Flags":                unsafe.Offsetof(hwParams{}.Flags),
		"hwParams.Masks":                unsafe.Offsetof(hwParams{}.Masks),
		"hwParams.MRes":                 unsafe.Offsetof(hwParams{}.MRes),
		"hwParams.Intervals":            unsafe.Offsetof(hwParams{}.Intervals),
		"hwParams.IRes":                 unsafe.Offsetof(hwParams{}.IRes),
		"hwParams.RMask":                unsafe.Offsetof(hwParams{}.RMask),
		"hwParams.CMask":                unsafe.Offsetof(hwParams{}.CMask),
		"hwParams.Info":                 unsafe.Offsetof(hwParams{}.Info),
		"hwParams.MSBits":               unsafe.Offsetof(hwParams{}.MSBits),
		"hwParams.RateNum":              unsafe.Offsetof(hwParams{}.RateNum),
		"hwParams.RateDen":              unsafe.Offsetof(hwParams{}.RateDen),
		"hwParams.FifoSize":             unsafe.Offsetof(hwParams{}.FifoSize),
		"hwParams.Sync":                 unsafe.Offsetof(hwParams{}.Sync),
		"hwParams.Reserved":             unsafe.Offsetof(hwParams{}.Reserved),
		"swParams.size":                 unsafe.Sizeof(swParams{}),
		"swParams.align":                unsafe.Alignof(swParams{}),
		"swParams.TimestampMode":        unsafe.Offsetof(swParams{}.TimestampMode),
		"swParams.PeriodStep":           unsafe.Offsetof(swParams{}.PeriodStep),
		"swParams.SleepMin":             unsafe.Offsetof(swParams{}.SleepMin),
		"swParams.AvailableMin":         unsafe.Offsetof(swParams{}.AvailableMin),
		"swParams.TransferAlign":        unsafe.Offsetof(swParams{}.TransferAlign),
		"swParams.StartThreshold":       unsafe.Offsetof(swParams{}.StartThreshold),
		"swParams.StopThreshold":        unsafe.Offsetof(swParams{}.StopThreshold),
		"swParams.SilenceThreshold":     unsafe.Offsetof(swParams{}.SilenceThreshold),
		"swParams.SilenceSize":          unsafe.Offsetof(swParams{}.SilenceSize),
		"swParams.Boundary":             unsafe.Offsetof(swParams{}.Boundary),
		"swParams.Protocol":             unsafe.Offsetof(swParams{}.Protocol),
		"swParams.TimestampType":        unsafe.Offsetof(swParams{}.TimestampType),
		"swParams.Reserved":             unsafe.Offsetof(swParams{}.Reserved),
		"transfer.size":                 unsafe.Sizeof(transfer{}),
		"transfer.align":                unsafe.Alignof(transfer{}),
		"transfer.Result":               unsafe.Offsetof(transfer{}.Result),
		"transfer.Buffer":               unsafe.Offsetof(transfer{}.Buffer),
		"transfer.Frames":               unsafe.Offsetof(transfer{}.Frames),
		"status.size":                   unsafe.Sizeof(status{}),
		"status.align":                  unsafe.Alignof(status{}),
		"status.State":                  unsafe.Offsetof(status{}.State),
		"status.TriggerTimestamp":       unsafe.Offsetof(status{}.TriggerTimestamp),
		"status.Timestamp":              unsafe.Offsetof(status{}.Timestamp),
		"status.ApplicationPointer":     unsafe.Offsetof(status{}.ApplicationPointer),
		"status.HardwarePointer":        unsafe.Offsetof(status{}.HardwarePointer),
		"status.Delay":                  unsafe.Offsetof(status{}.Delay),
		"status.Available":              unsafe.Offsetof(status{}.Available),
		"status.AvailableMax":           unsafe.Offsetof(status{}.AvailableMax),
		"status.Overrange":              unsafe.Offsetof(status{}.Overrange),
		"status.SuspendedState":         unsafe.Offsetof(status{}.SuspendedState),
		"status.AudioTimestampData":     unsafe.Offsetof(status{}.AudioTimestampData),
		"status.AudioTimestamp":         unsafe.Offsetof(status{}.AudioTimestamp),
		"status.DriverTimestamp":        unsafe.Offsetof(status{}.DriverTimestamp),
		"status.AudioTimestampAccuracy": unsafe.Offsetof(status{}.AudioTimestampAccuracy),
		"status.Reserved":               unsafe.Offsetof(status{}.Reserved),
		"ioctlHWRefine":                 uintptr(ioctlHWRefine),
		"ioctlHWParams":                 uintptr(ioctlHWParams),
		"ioctlSWParams":                 uintptr(ioctlSWParams),
		"ioctlStatus":                   uintptr(ioctlStatus),
		"ioctlPrepare":                  uintptr(ioctlPrepare),
		"ioctlDrop":                     uintptr(ioctlDrop),
		"ioctlRead":                     uintptr(ioctlRead),
		"paramAccess":                   uintptr(paramAccess),
		"paramFormat":                   uintptr(paramFormat),
		"paramSubformat":                uintptr(paramSubformat),
		"paramChannels":                 uintptr(paramChannels),
		"paramRate":                     uintptr(paramRate),
		"paramPeriodTime":               uintptr(paramPeriodTime),
		"paramPeriodSize":               uintptr(paramPeriodSize),
		"paramPeriods":                  uintptr(paramPeriods),
		"paramBufferSize":               uintptr(paramBufferSize),
		"firstInterval":                 uintptr(firstInterval),
		"accessInterleaved":             uintptr(accessInterleaved),
		"formatS16LE":                   uintptr(formatS16LE),
		"subformatStandard":             uintptr(subformatStandard),
		"protocolVersion":               uintptr(protocolVersion),
		"timestampEnable":               uintptr(timestampEnable),
		"timestampMonotonic":            uintptr(timestampMonotonic),
		"stateRunning":                  uintptr(stateRunning),
		"stateXRun":                     uintptr(stateXRun),
		"stateSuspended":                uintptr(stateSuspended),
		"stateDisconnected":             uintptr(stateDisconnected),
		"interval.Flags":                unsafe.Offsetof(interval{}.Flags),
		"interval.FlagBits":             uintptr(intervalOpenMin | intervalOpenMax | intervalInteger | intervalEmpty),
	}
	data, err := os.ReadFile("testdata/abi-linux-amd64.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Fields(string(data)) {
		key, text, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid ABI fixture line %q", line)
		}
		want, err := strconv.ParseUint(text, 10, 64)
		got, found := actual[key]
		if err != nil || !found || uint64(got) != want {
			t.Errorf("ABI %s=%d want %s (error %v)", key, got, text, err)
		}
		delete(actual, key)
	}
	if len(actual) != 0 {
		t.Fatalf("missing C measurements: %v", actual)
	}
}
