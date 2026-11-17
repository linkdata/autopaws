//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "autopaws-kvm is not implemented yet")
	os.Exit(1)
}
