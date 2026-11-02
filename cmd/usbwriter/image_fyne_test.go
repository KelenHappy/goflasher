//go:build fyne && (linux || windows || darwin)

package main

import (
	"errors"
	"testing"

	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/osinfo"
)

func TestSystemDetails(t *testing.T) {
	tr := i18n.New("zh-TW")
	windows := osinfo.System{Family: osinfo.Windows, Name: "Windows 11", Version: "24H2", Build: "26100.1742", Arch: "x86_64",
		Editions: []string{"Windows 11 Home", "Windows 11 Pro"}}
	for name, tc := range map[string]struct {
		system osinfo.System
		err    error
		want   string
	}{
		"windows":  {windows, nil, "作業系統：Windows 11 24H2 (build 26100.1742, x86_64)\n可安裝版本：Windows 11 Home, Windows 11 Pro"},
		"fedora":   {osinfo.System{Family: osinfo.Linux, Name: "Fedora Workstation", Version: "44"}, nil, "作業系統：Fedora Workstation 44"},
		"unknown":  {osinfo.System{Label: "DATA"}, nil, "作業系統：無法辨識"},
		"read err": {windows, errors.New("broken"), "作業系統：無法辨識"},
	} {
		if got := systemDetails(tr, tc.system, tc.err); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestOnlyUncompressedISOsAreIdentified(t *testing.T) {
	for _, tc := range []struct {
		info image.Info
		want bool
	}{
		{image.Info{Format: image.FormatISO, Compression: image.CompressionNone}, true},
		{image.Info{Format: image.FormatISO, Compression: image.CompressionXZ}, false},
		{image.Info{Format: image.FormatIMG, Compression: image.CompressionNone}, false},
	} {
		if got := identifiable(tc.info); got != tc.want {
			t.Errorf("%+v: got %v", tc.info, got)
		}
	}
}
