package app

import (
	"errors"
	"testing"

	"github.com/goflasher/goflasher/internal/disk"
	"github.com/goflasher/goflasher/internal/installer"
	"github.com/goflasher/goflasher/internal/verify"
	"github.com/goflasher/goflasher/internal/wim/native"
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
