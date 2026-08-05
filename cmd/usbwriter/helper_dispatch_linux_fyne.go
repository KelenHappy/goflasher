//go:build linux && fyne

package main

import (
	"fmt"
	"os"

	linuxbackend "github.com/goflasher/goflasher/internal/linux"
)

func dispatchEmbeddedHelper() bool {
	if !linuxbackend.IsEmbeddedHelperInvocation(os.Args) {
		return false
	}
	if err := linuxbackend.RunPrivilegedHelper(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
