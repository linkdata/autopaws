//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

func anyParams() hwParams {
	var params hwParams
	for i := range params.Masks {
		for j := range params.Masks[i].Bits {
			params.Masks[i].Bits[j] = ^uint32(0)
		}
	}
	for i := range params.Intervals {
		params.Intervals[i] = interval{Min: 0, Max: ^uint32(0)}
	}
	params.RMask = ^uint32(0)
	return params
}

func (params *hwParams) setMask(parameter, value int) {
	params.Masks[parameter] = mask{}
	params.Masks[parameter].Bits[value/32] = 1 << (value % 32)
	params.RMask |= 1 << parameter
}

func (params *hwParams) setRange(parameter int, minimum, maximum uint32) {
	params.Intervals[parameter-firstInterval] = interval{minimum, maximum, intervalInteger}
	params.RMask |= 1 << parameter
}

func (params *hwParams) single(parameter int) (uint32, error) {
	interval := params.Intervals[parameter-firstInterval]
	if interval.Min != interval.Max || interval.Flags&(intervalEmpty|intervalOpenMin|intervalOpenMax) != 0 {
		return 0, ErrSettings
	}
	return interval.Min, nil
}

func (params *hwParams) exactMask(parameter, value int) bool {
	var expected mask
	expected.Bits[value/32] = 1 << (value % 32)
	return params.Masks[parameter] == expected
}

func negotiate(ctx context.Context, ops kernel, fd int) (Config, error) {
	base := anyParams()
	base.setMask(paramAccess, accessInterleaved)
	base.setMask(paramFormat, formatS16LE)
	base.setMask(paramSubformat, subformatStandard)
	if err := call(ctx, ops, fd, ioctlHWRefine, unsafe.Pointer(&base)); err != nil {
		return Config{}, fmt.Errorf("HW_REFINE S16_LE: %w", err)
	}
	var selected hwParams
	var found bool
	var selectedChannels, selectedRate uint32
	for _, channels := range []uint32{2, 1} {
		candidate := base
		candidate.setRange(paramChannels, channels, channels)
		candidate.setRange(paramPeriodTime, 20_000, 100_000)
		candidate.setRange(paramPeriods, 4, 8)
		if err := call(ctx, ops, fd, ioctlHWRefine, unsafe.Pointer(&candidate)); err != nil {
			if errors.Is(err, syscall.EINVAL) && ctx.Err() == nil {
				continue
			}
			return Config{}, fmt.Errorf("HW_REFINE channels: %w", err)
		}
		selected, found, selectedChannels = candidate, true, channels
		break
	}
	if !found {
		return Config{}, ErrSettings
	}
	preferred := selected
	preferred.setRange(paramRate, 48_000, 48_000)
	if err := call(ctx, ops, fd, ioctlHWRefine, unsafe.Pointer(&preferred)); err == nil {
		selected = preferred
		selectedRate = 48_000
	} else if !errors.Is(err, syscall.EINVAL) || ctx.Err() != nil {
		return Config{}, fmt.Errorf("HW_REFINE rate: %w", err)
	}
	// Leaving the rate interval refined (rather than pinning its lower bound)
	// lets HW_PARAMS choose a supported point even for discrete-rate devices.
	if err := call(ctx, ops, fd, ioctlHWParams, unsafe.Pointer(&selected)); err != nil {
		return Config{}, fmt.Errorf("HW_PARAMS: %w", err)
	}
	config, err := negotiatedConfig(&selected)
	if err != nil {
		return Config{}, err
	}
	if config.Channels != selectedChannels || selectedRate != 0 && config.Rate != selectedRate {
		return Config{}, ErrSettings
	}
	software := swParams{
		TimestampMode: timestampEnable, TimestampType: timestampMonotonic,
		PeriodStep:   1,
		AvailableMin: uint64(config.PeriodFrames), StartThreshold: 1,
		StopThreshold: uint64(config.BufferFrames), Protocol: protocolVersion,
	}
	if err := call(ctx, ops, fd, ioctlSWParams, unsafe.Pointer(&software)); err != nil {
		return Config{}, fmt.Errorf("SW_PARAMS: %w", err)
	}
	if software.TimestampMode != timestampEnable || software.TimestampType != timestampMonotonic || software.AvailableMin != uint64(config.PeriodFrames) ||
		software.StartThreshold != 1 || software.StopThreshold != uint64(config.BufferFrames) {
		return Config{}, ErrSettings
	}
	if err := call(ctx, ops, fd, ioctlPrepare, nil); err != nil {
		return Config{}, fmt.Errorf("PREPARE: %w", err)
	}
	return config, nil
}

func negotiatedConfig(params *hwParams) (Config, error) {
	if !params.exactMask(paramAccess, accessInterleaved) || !params.exactMask(paramFormat, formatS16LE) || !params.exactMask(paramSubformat, subformatStandard) {
		return Config{}, ErrSettings
	}
	var config Config
	var err error
	if config.Rate, err = params.single(paramRate); err != nil {
		return Config{}, err
	}
	if config.Channels, err = params.single(paramChannels); err != nil {
		return Config{}, err
	}
	if config.PeriodFrames, err = params.single(paramPeriodSize); err != nil {
		return Config{}, err
	}
	if config.BufferFrames, err = params.single(paramBufferSize); err != nil {
		return Config{}, err
	}
	periods, err := params.single(paramPeriods)
	if err != nil || periods < 4 || periods > 8 || config.Rate == 0 || config.Channels < 1 || config.Channels > 2 ||
		config.PeriodFrames == 0 || config.PeriodFrames > 1<<20 || config.BufferFrames < config.PeriodFrames || uint64(config.BufferFrames) > uint64(config.PeriodFrames)*8 {
		return Config{}, ErrSettings
	}
	duration, err := framesNS(uint64(config.PeriodFrames), config.Rate)
	if err != nil || duration > int64(ServerQueueBudget) || duration < int64(20*time.Millisecond) {
		return Config{}, ErrSettings
	}
	return config, nil
}
