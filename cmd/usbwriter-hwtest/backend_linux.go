//go:build linux

package main

import (
	"github.com/goflasher/goflasher/internal/device"
	linuxbackend "github.com/goflasher/goflasher/internal/linux"
)

func newBackend() device.Backend { return linuxbackend.NewBackend() }
