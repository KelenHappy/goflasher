package app

import (
	"context"
	"io"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/progress"
)

// WriterService is UI-independent and can later be implemented using a
// privileged Linux backend without granting privileges to the GUI process.
type WriterService interface {
	Write(context.Context, image.Info, device.Device, bool, bool, chan<- progress.Update) error
}

// ImageOpener abstracts image ownership for services that must close every
// successfully opened stream before the destructive workflow returns.
type ImageOpener interface {
	Open(image.Info) (io.ReadCloser, error)
}
