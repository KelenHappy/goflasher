//go:build windows

package wim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func windowsSplitPaths(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "paths with spaces")
	output := filepath.Join(root, "split output")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source image.wim")
	mustWriteFile(t, source, "wim")
	return source, output
}

func fakeWindowsBackend(t *testing.T, run func(context.Context, string, []string) ([]byte, error)) {
	t.Helper()
	oldResolve, oldExecute := resolveDISM, executeDISM
	resolveDISM = func() (string, error) { return `C:\Windows\System32\dism.exe`, nil }
	executeDISM = run
	t.Cleanup(func() { resolveDISM, executeDISM = oldResolve, oldExecute })
}

func TestProbeUsesOnlyTrustedResolver(t *testing.T) {
	old := resolveDISM
	t.Cleanup(func() { resolveDISM = old })
	resolveDISM = func() (string, error) { return `C:\Windows\System32\dism.exe`, nil }
	if err := Probe(); err != nil {
		t.Fatal(err)
	}
	resolveDISM = func() (string, error) { return "", os.ErrNotExist }
	if err := Probe(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
}

func TestTrustedDISMPathDoesNotSearchPATH(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "System32"), 0700); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pathDir, "dism.exe"), "fake")
	t.Setenv("SystemRoot", root)
	t.Setenv("WINDIR", "")
	t.Setenv("PATH", pathDir)
	if _, err := trustedDISMPath(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PATH executable accepted: %v", err)
	}
	trusted := filepath.Join(root, "System32", "dism.exe")
	mustWriteFile(t, trusted, "trusted")
	got, err := trustedDISMPath()
	if err != nil {
		t.Fatalf("path=%q error=%v", got, err)
	}
	assertSameFile(t, got, trusted)
}

func TestDISMArgumentsAreSeparateExactAndSizeRoundsDown(t *testing.T) {
	source, output := windowsSplitPaths(t)
	var gotExecutable string
	var gotArgs []string
	fakeWindowsBackend(t, func(_ context.Context, executable string, args []string) ([]byte, error) {
		gotExecutable, gotArgs = executable, append([]string(nil), args...)
		return nil, os.WriteFile(filepath.Join(output, "install.swm"), []byte("part"), 0600)
	})
	var progress [][2]uint64
	_, err := Split(context.Background(), Request{SourcePath: source, OutputDir: output, PartSize: 5*1024*1024 + 999, Progress: func(a, b uint64) { progress = append(progress, [2]uint64{a, b}) }})
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, canonicalOutput := mustCanonical(t, source), mustCanonical(t, output)
	want := []string{"/English", "/Split-Image", "/ImageFile:" + canonicalSource, "/SWMFile:" + filepath.Join(canonicalOutput, "install.swm"), "/FileSize:5"}
	assertDISMInvocation(t, gotExecutable, gotArgs, want)
	assertProgress(t, progress, [2]uint64{0, 1}, [2]uint64{1, 1})
}

func TestDISMRejectsSubMiBPartSizeWithoutExecution(t *testing.T) {
	source, output := windowsSplitPaths(t)
	called := false
	fakeWindowsBackend(t, func(context.Context, string, []string) ([]byte, error) { called = true; return nil, nil })
	if _, err := Split(context.Background(), Request{SourcePath: source, OutputDir: output, PartSize: 1024*1024 - 1}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if called {
		t.Fatal("DISM executed")
	}
}

func TestDISMFailureAndCancellationCleanPartialOutput(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancel], func(t *testing.T) {
			source, output := windowsSplitPaths(t)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			fakeWindowsBackend(t, func(context.Context, string, []string) ([]byte, error) {
				_ = os.WriteFile(filepath.Join(output, "install.swm"), []byte("partial"), 0600)
				if cancel {
					stop()
				}
				return []byte("diagnostic"), errors.New("exit status 1")
			})
			var progress [][2]uint64
			_, err := Split(ctx, Request{SourcePath: source, OutputDir: output, PartSize: 2 * 1024 * 1024, Progress: func(a, b uint64) { progress = append(progress, [2]uint64{a, b}) }})
			assertSplitFailed(t, err, cancel)
			if _, statErr := os.Stat(filepath.Join(output, "install.swm")); !os.IsNotExist(statErr) {
				t.Fatalf("partial remains: %v", statErr)
			}
			assertProgress(t, progress, [2]uint64{0, 1})
		})
	}
}

func TestDISMSuccessStillValidatesAndCleansOutput(t *testing.T) {
	source, output := windowsSplitPaths(t)
	fakeWindowsBackend(t, func(context.Context, string, []string) ([]byte, error) {
		return nil, os.WriteFile(filepath.Join(output, "install2.swm"), []byte("gap"), 0600)
	})
	if _, err := Split(context.Background(), Request{SourcePath: source, OutputDir: output, PartSize: 2 * 1024 * 1024}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "install2.swm")); !os.IsNotExist(err) {
		t.Fatalf("invalid output remains: %v", err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func mustCanonical(t *testing.T, path string) string {
	t.Helper()
	canonical, err := canonicalAbsolute(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

// assertSameFile checks that got and want name the same file on disk.
func assertSameFile(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("resolved DISM %q is not trusted file %q", got, want)
	}
}

// assertDISMInvocation checks that the trusted DISM binary ran directly, not
// through a shell, with exactly the wanted arguments.
func assertDISMInvocation(t *testing.T, executable string, args, want []string) {
	t.Helper()
	if executable != `C:\Windows\System32\dism.exe` || !slices.Equal(args, want) {
		t.Fatalf("executable=%q args=%q", executable, args)
	}
	if args[0] == "cmd.exe" || args[0] == "powershell.exe" {
		t.Fatal("shell used")
	}
}

// assertSplitFailed checks that Split reported an error and that a canceled run
// surfaces context.Canceled.
func assertSplitFailed(t *testing.T, err error, canceled bool) {
	t.Helper()
	if err == nil {
		t.Fatal("Split succeeded, want an error")
	}
	if canceled && !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

// assertProgress checks that the callback fired synchronously with exactly the
// wanted done/total pairs, in order.
func assertProgress(t *testing.T, got [][2]uint64, want ...[2]uint64) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("progress=%v, want %v", got, want)
	}
}
