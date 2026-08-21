package app

import (
	"context"
	"errors"

	"github.com/goflasher/goflasher/internal/disk"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
	"github.com/goflasher/goflasher/internal/verify"
	"github.com/goflasher/goflasher/internal/wim/native"
	"github.com/goflasher/goflasher/internal/writer"
)

// ErrorMessageID preserves actionable installer failure categories for the UI.
func ErrorMessageID(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "error.cancelled"
	case errors.Is(err, native.ErrABIMismatch):
		return "error.libwim_abi"
	case errors.Is(err, ErrInstallerBuilderUnavailable):
		return "error.libwim_unavailable"
	case errors.Is(err, installer.ErrMissingUEFILoader):
		return "error.uefi_loader"
	case errors.Is(err, installer.ErrTemporarySpace):
		return "error.temporary_space"
	case errors.Is(err, installer.ErrTargetTooSmall), errors.Is(err, writer.ErrTargetTooSmall):
		return "error.target_small"
	case errors.Is(err, installer.ErrWIMSplitFailure):
		return "error.wim_split"
	case errors.Is(err, verify.ErrGPTLayout):
		return "error.gpt_verify"
	case errors.Is(err, verify.ErrFAT32Filesystem):
		return "error.fat_verify"
	case errors.Is(err, disk.ErrChanged):
		return "error.device_changed"
	case errors.Is(err, image.ErrUnsafeClassification), errors.Is(err, installer.ErrUnsupported):
		return "error.iso_unsupported"
	default:
		return "error.operation"
	}
}
