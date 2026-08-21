//go:build !windows

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

func availableTemporarySpace() (uint64, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(os.TempDir(), &stat); err != nil || stat.Bsize <= 0 {
		return 0, false
	}
	return stat.Bavail * uint64(stat.Bsize), true
}
