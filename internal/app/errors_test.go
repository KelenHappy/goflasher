package app

import (
	"context"
	"errors"
	"testing"

	"github.com/goflasher/goflasher/internal/disk"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
	"github.com/goflasher/goflasher/internal/verify"
	"github.com/goflasher/goflasher/internal/wim/native"
	"github.com/goflasher/goflasher/internal/writer"
)

func TestInstallerErrorsRemainUserVisibleAndDistinct(t *testing.T) {
	tests := map[error]string{
		installer.ErrMissingUEFILoader: "error.uefi_loader",
		installer.ErrTargetTooSmall:    "error.target_small",
		installer.ErrTemporarySpace:    "error.temporary_space",
		native.ErrABIMismatch:          "error.libwim_abi",
		installer.ErrWIMSplitFailure:   "error.wim_split",
		verify.ErrGPTLayout:            "error.gpt_verify",
		verify.ErrFAT32Filesystem:      "error.fat_verify",
		disk.ErrChanged:                "error.device_changed",
	}
	seen := map[string]bool{}
	for err, want := range tests {
		got := ErrorMessageID(errors.Join(errors.New("operation failed"), err))
		if got != want {
			t.Errorf("%v => %q, want %q", err, got, want)
		}
		if seen[got] {
			t.Errorf("duplicate user-visible category %q", got)
		}
		seen[got] = true
	}
}

func TestErrorMessageIDPrecedenceAndFallback(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"cancel beats any cause":       {errors.Join(installer.ErrWIMSplitFailure, context.Canceled), "error.cancelled"},
		"ABI mismatch beats available": {errors.Join(ErrInstallerBuilderUnavailable, native.ErrABIMismatch), "error.libwim_abi"},
		"builder unavailable":          {ErrInstallerBuilderUnavailable, "error.libwim_unavailable"},
		"raw target too small":         {writer.ErrTargetTooSmall, "error.target_small"},
		"unsafe classification":        {image.ErrUnsafeClassification, "error.iso_unsupported"},
		"unsupported installer":        {installer.ErrUnsupported, "error.iso_unsupported"},
		"uncategorized":                {errors.New("boom"), "error.operation"},
		"nil":                          {nil, "error.operation"},
	} {
		if got := ErrorMessageID(tc.err); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
