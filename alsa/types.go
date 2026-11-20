// Package alsa defines PCM types and Linux ALSA capture.
// FFmpeg capture on macOS shares these PCM types.
package alsa

import (
	"errors"
	"time"
)

var (
	ErrClosed       = errors.New("alsa: capture closed")
	ErrBusy         = errors.New("alsa: capture busy; PipeWire or WirePlumber may own the PCM")
	ErrDisconnected = errors.New("alsa: capture device disconnected")
	ErrSettings     = errors.New("alsa: unsupported or invalid PCM settings")
	ErrSamples      = errors.New("alsa: invalid PCM frame count")
	ErrTimestamp    = errors.New("alsa: invalid monotonic capture timestamp")
)

// ServerQueueBudget leaves the other 100 ms of the 200 ms application-queue
// ceiling for the browser worklet. This excludes kernel/network/output latency.
const ServerQueueBudget = 100 * time.Millisecond

// Config is immutable for a generation. Samples are always signed 16-bit,
// little-endian, interleaved. Reopen and recovery allocate new generations.
type Config struct {
	Rate, Channels             uint32
	PeriodFrames, BufferFrames uint32
	Generation                 uint64
}

// Block contains only Frames valid sample frames. Data belongs to its receiver
// and survives future reads, other subscriptions and capture Close.
// TimestampNS estimates the first sample's CLOCK_MONOTONIC capture time from
// ALSA's timestamp/delay snapshot on Linux. FFmpeg macOS capture instead
// estimates it from pipe receipt; FFmpeg/OS buffering is unmeasured. Neither is
// a calendar timestamp. Hardware/driver reporting also limits Linux accuracy.
type Block struct {
	Data        []byte
	Sequence    uint64
	TimestampNS int64
	Frames      uint32
	Config      Config
}

// Stats counts observed overrun and suspend recovery events.
type Stats struct{ Overruns, Suspends uint64 }
