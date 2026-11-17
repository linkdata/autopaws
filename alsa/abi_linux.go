//go:build linux && (amd64 || arm64)

package alsa

import (
	"syscall"
	"unsafe"
)

// Linux LP64 UAPI mirrors; testdata/sz.c records every named offset.
type (
	mask     struct{ Bits [8]uint32 }
	interval struct{ Min, Max, Flags uint32 }
	hwParams struct {
		Flags            uint32
		Masks            [3]mask
		MRes             [5]mask
		Intervals        [12]interval
		IRes             [9]interval
		RMask, CMask     uint32
		Info, MSBits     uint32
		RateNum, RateDen uint32
		FifoSize         uint64
		Sync             [16]byte
		Reserved         [48]byte
	}
)

type swParams struct {
	TimestampMode    int32
	PeriodStep       uint32
	SleepMin         uint32
	_                uint32
	AvailableMin     uint64
	TransferAlign    uint64
	StartThreshold   uint64
	StopThreshold    uint64
	SilenceThreshold uint64
	SilenceSize      uint64
	Boundary         uint64
	Protocol         uint32
	TimestampType    uint32
	Reserved         [56]byte
}

type transfer struct {
	Result int64
	Buffer unsafe.Pointer
	Frames uint64
}

type status struct {
	State                  int32
	_                      uint32
	TriggerTimestamp       syscall.Timespec
	Timestamp              syscall.Timespec
	ApplicationPointer     uint64
	HardwarePointer        uint64
	Delay                  int64
	Available              uint64
	AvailableMax           uint64
	Overrange              uint64
	SuspendedState         int32
	AudioTimestampData     uint32
	AudioTimestamp         syscall.Timespec
	DriverTimestamp        syscall.Timespec
	AudioTimestampAccuracy uint32
	Reserved               [20]byte
}

const (
	ioctlHWRefine = 0xc2604110
	ioctlHWParams = 0xc2604111
	ioctlSWParams = 0xc0884113
	ioctlStatus   = 0x80984120
	ioctlPrepare  = 0x00004140
	ioctlDrop     = 0x00004143
	ioctlRead     = 0x80184151

	paramAccess     = 0
	paramFormat     = 1
	paramSubformat  = 2
	paramChannels   = 10
	paramRate       = 11
	paramPeriodTime = 12
	paramPeriodSize = 13
	paramPeriods    = 15
	paramBufferSize = 17
	firstInterval   = 8

	accessInterleaved  = 3
	formatS16LE        = 2
	subformatStandard  = 0
	intervalOpenMin    = 1
	intervalOpenMax    = 2
	intervalInteger    = 4
	intervalEmpty      = 8
	protocolVersion    = 0x20012
	timestampEnable    = 1
	timestampMonotonic = 1
	stateRunning       = 3
	stateXRun          = 4
	stateSuspended     = 7
	stateDisconnected  = 8
)
