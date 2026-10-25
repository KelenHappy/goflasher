//go:build fyne && (linux || windows || darwin)

package main

import (
	"errors"
	"fmt"
	"runtime/debug"
)

// errInternal marks an error recovered from a panic in background work.
var errInternal = errors.New("internal error")

// runRecovered runs work on the calling goroutine, turning a panic into an
// error so the operation reports its cause instead of closing the application
// with the log unsaved. The stack goes to the exportable history; the caller's
// usual error path shows the failure to the user.
func (c *guiController) runRecovered(operation string, work func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Debug("panic recovered", "operation", operation, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("%w during %s: %v", errInternal, operation, r)
		}
	}()
	return work()
}
