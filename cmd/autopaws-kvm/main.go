//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, commandHooks{})
	stop()
	os.Exit(code)
}
