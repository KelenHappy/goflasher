//go:build windows

package main

import (
	"github.com/goflasher/goflasher/internal/device"
	windowsbackend "github.com/goflasher/goflasher/internal/windows"
)

func newBackend() device.Backend { return windowsbackend.NewBackend() }
