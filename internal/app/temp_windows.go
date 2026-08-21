//go:build windows

package app

func availableTemporarySpace() (uint64, bool) { return 0, false }
