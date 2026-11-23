//go:build (linux || darwin || windows) && (amd64 || arm64)

package main

import (
	"context"
	"io"

	"github.com/linkdata/autopaws/devices"
)

func listDevicesRuntime(ctx context.Context, output io.Writer) error {
	return listDevicesWith(ctx, output, devices.Options{})
}

func listDevicesWith(ctx context.Context, output io.Writer, options devices.Options) error {
	inventory, err := devices.Discover(ctx, options)
	if err != nil {
		return err
	}
	return devices.WriteList(output, inventory)
}
