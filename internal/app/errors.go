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

// errorMessages maps failure categories to UI message IDs. Order matters: an
// error can wrap several categories, and the first match wins, so the more
// specific cause (an ABI mismatch) precedes the general one (the builder
// being unavailable).
var errorMessages = []struct {
	causes []error
	id     string
}{
	{[]error{context.Canceled}, "error.cancelled"},
	{[]error{native.ErrABIMismatch}, "error.libwim_abi"},
	{[]error{ErrInstallerBuilderUnavailable}, "error.libwim_unavailable"},
	{[]error{installer.ErrMissingUEFILoader}, "error.uefi_loader"},
	{[]error{installer.ErrTemporarySpace}, "error.temporary_space"},
	{[]error{installer.ErrTargetTooSmall, writer.ErrTargetTooSmall}, "error.target_small"},
	{[]error{installer.ErrWIMSplitFailure}, "error.wim_split"},
	{[]error{verify.ErrGPTLayout}, "error.gpt_verify"},
	{[]error{verify.ErrFAT32Filesystem}, "error.fat_verify"},
	{[]error{disk.ErrChanged}, "error.device_changed"},
	{[]error{image.ErrUnsafeClassification, installer.ErrUnsupported}, "error.iso_unsupported"},
}

// ErrorMessageID preserves actionable installer failure categories for the UI.
func ErrorMessageID(err error) string {
	for _, m := range errorMessages {
		if isAny(err, m.causes) {
			return m.id
		}
	}
	return "error.operation"
}

func isAny(err error, targets []error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
