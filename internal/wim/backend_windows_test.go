//go:build windows

package wim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	if err := os.WriteFile(source, []byte("wim"), 0600); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(pathDir, "dism.exe"), []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SystemRoot", root)
	t.Setenv("WINDIR", "")
	t.Setenv("PATH", pathDir)
	if _, err := trustedDISMPath(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PATH executable accepted: %v", err)
	}
	trusted := filepath.Join(root, "System32", "dism.exe")
	if err := os.WriteFile(trusted, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := trustedDISMPath()
	if err != nil {
		t.Fatalf("path=%q error=%v", got, err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	trustedInfo, err := os.Stat(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, trustedInfo) {
		t.Fatalf("resolved DISM %q is not trusted file %q", got, trusted)
	}
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
	_, err := Split(context.Background(), source, output, 5*1024*1024+999, func(a, b uint64) { progress = append(progress, [2]uint64{a, b}) })
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := canonicalAbsolute(source)
	if err != nil {
		t.Fatal(err)
	}
	canonicalOutput, err := canonicalAbsolute(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/English", "/Split-Image", "/ImageFile:" + canonicalSource, "/SWMFile:" + filepath.Join(canonicalOutput, "install.swm"), "/FileSize:5"}
	if gotExecutable != `C:\Windows\System32\dism.exe` || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("executable=%q args=%q", gotExecutable, gotArgs)
	}
	if reflect.DeepEqual(gotArgs[0], "cmd.exe") || reflect.DeepEqual(gotArgs[0], "powershell.exe") {
		t.Fatal("shell used")
	}
	if !reflect.DeepEqual(progress, [][2]uint64{{0, 1}, {1, 1}}) {
		t.Fatalf("progress=%v", progress)
	}
}

func TestDISMRejectsSubMiBPartSizeWithoutExecution(t *testing.T) {
	source, output := windowsSplitPaths(t)
	called := false
	fakeWindowsBackend(t, func(context.Context, string, []string) ([]byte, error) { called = true; return nil, nil })
	if _, err := Split(context.Background(), source, output, 1024*1024-1, nil); !errors.Is(err, ErrUnsupported) {
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
			_, err := Split(ctx, source, output, 2*1024*1024, func(a, b uint64) { progress = append(progress, [2]uint64{a, b}) })
			if err == nil || (cancel && !errors.Is(err, context.Canceled)) {
				t.Fatalf("error=%v", err)
			}
			if _, statErr := os.Stat(filepath.Join(output, "install.swm")); !os.IsNotExist(statErr) {
				t.Fatalf("partial remains: %v", statErr)
			}
			if !reflect.DeepEqual(progress, [][2]uint64{{0, 1}}) {
				t.Fatalf("progress=%v", progress)
			}
		})
	}
}

func TestDISMSuccessStillValidatesAndCleansOutput(t *testing.T) {
	source, output := windowsSplitPaths(t)
	fakeWindowsBackend(t, func(context.Context, string, []string) ([]byte, error) {
		return nil, os.WriteFile(filepath.Join(output, "install2.swm"), []byte("gap"), 0600)
	})
	if _, err := Split(context.Background(), source, output, 2*1024*1024, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "install2.swm")); !os.IsNotExist(err) {
		t.Fatalf("invalid output remains: %v", err)
	}
}
