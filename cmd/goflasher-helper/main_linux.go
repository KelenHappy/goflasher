//go:build linux

package main

import (
	"fmt"
	"os"

	linuxbackend "github.com/goflasher/goflasher/internal/linux"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "arguments are not accepted")
		os.Exit(2)
	}
	if err := linuxbackend.RunPrivilegedHelper(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
