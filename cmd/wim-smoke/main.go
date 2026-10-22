// Command wim-smoke is a packaging-time ABI smoke test, not a runtime data
// processing CLI. Release CI uses it to exercise the exact PureGo load/open/
// split path that the application uses.
package main

import (
	"context"
	"errors"
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
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "split smoke failed:", err)
		os.Exit(1)
	}
}

func run(source string) error {
	if err := wim.Probe(); err != nil {
		return err
	}
	out, err := os.MkdirTemp("", "goflasher-wim-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(out)
	var progress progressLog
	parts, err := wim.Split(context.Background(), wim.Request{SourcePath: source, OutputDir: out, PartSize: 4 << 20, Progress: progress.record})
	if err != nil {
		return fmt.Errorf("error=%w", err)
	}
	if err := checkParts(parts); err != nil {
		return err
	}
	return progress.check()
}

func checkParts(parts []wim.Part) error {
	if len(parts) == 0 {
		return errors.New("no parts")
	}
	if filepath.Base(parts[0].Path) != "install.swm" {
		return fmt.Errorf("unexpected parts=%v", parts)
	}
	return nil
}

// progressLog keeps the number of progress reports and the last one.
type progressLog struct {
	reports     int
	done, total uint64
}

func (p *progressLog) record(done, total uint64) {
	p.reports, p.done, p.total = p.reports+1, done, total
}

// check requires at least one report and, from the final SPLIT_END_PART
// message, every byte reported as done.
func (p *progressLog) check() error {
	if p.reports == 0 || p.total == 0 {
		return fmt.Errorf("no progress reports=%d total=%d", p.reports, p.total)
	}
	if p.done != p.total {
		return fmt.Errorf("progress reports=%d last=%d/%d", p.reports, p.done, p.total)
	}
	return nil
}
