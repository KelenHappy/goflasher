// Command wim-smoke is a packaging-time ABI smoke test, not a runtime data
// processing CLI. Release CI uses it to exercise the exact PureGo load/open/
// split path that the application uses.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goflasher/goflasher/internal/wim"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: wim-smoke INPUT.wim")
		os.Exit(64)
	}
	if err := wim.Probe(); err != nil {
		panic(err)
	}
	out, err := os.MkdirTemp("", "goflasher-wim-smoke-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(out)
	parts, err := wim.Split(context.Background(), os.Args[1], out, 4<<20, nil)
	if err != nil || len(parts) == 0 || filepath.Base(parts[0].Path) != "install.swm" {
		panic(fmt.Sprintf("split smoke failed: parts=%v error=%v", parts, err))
	}
}
