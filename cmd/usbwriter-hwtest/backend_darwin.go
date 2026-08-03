//go:build darwin

package main

import (
	"github.com/goflasher/goflasher/internal/device"
	macosbackend "github.com/goflasher/goflasher/internal/macos"
)

func newBackend() device.Backend { return macosbackend.NewBackend() }
