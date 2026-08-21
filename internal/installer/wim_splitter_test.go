package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/goflasher/goflasher/internal/wim"
)

func TestNativeWIMSplitterStagesRetainedStreamAndCleansEverything(t *testing.T) {
	payload := bytes.Repeat([]byte("retained-wim"), 100)
	sum := sha256.Sum256(payload)
	var temporary string
	splitter := &NativeWIMSplitter{split: func(_ context.Context, sourcePath, output string, partSize uint64, _ wim.ProgressFunc) ([]wim.Part, error) {
		temporary = filepath.Dir(sourcePath)
		info, err := os.Stat(temporary)
		if err != nil {
			return nil, err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatalf("temporary mode=%o", info.Mode().Perm())
		}
		info, err = os.Stat(sourcePath)
		if err != nil {
			return nil, err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("source mode=%o", info.Mode().Perm())
		}
		got, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(got, payload) || partSize != 3800<<20 {
			t.Fatal("staged source or policy differs")
		}
		first, second := filepath.Join(output, "install.swm"), filepath.Join(output, "install2.swm")
		if err := os.WriteFile(first, got[:700], 0600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(second, got[700:], 0600); err != nil {
			return nil, err
		}
		return []wim.Part{{Path: first, Size: 700}, {Path: second, Size: uint64(len(got) - 700)}}, nil
	}}
	var names []string
	err := splitter.Split(context.Background(), bytes.NewReader(payload), uint64(len(payload)), hex.EncodeToString(sum[:]), 3800<<20, func(part SplitPart) error {
		names = append(names, part.Name)
		got, err := os.ReadFile(part.Data.(*os.File).Name())
		if err != nil || uint64(len(got)) != part.Size {
			t.Fatalf("part read=%d error=%v", len(got), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "install.swm" || names[1] != "install2.swm" {
		t.Fatalf("names=%v", names)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestNativeWIMPrepareCompletesBeforePartsAreConsumed(t *testing.T) {
	payload := bytes.Repeat([]byte("preflight-wim"), 100)
	sum := sha256.Sum256(payload)
	var temporary string
	splittingReported := false
	splitter := &NativeWIMSplitter{split: func(_ context.Context, sourcePath, output string, _ uint64, _ wim.ProgressFunc) ([]wim.Part, error) {
		if !splittingReported {
			t.Fatal("native split started before splitting progress")
		}
		temporary = filepath.Dir(sourcePath)
		part := filepath.Join(output, "install.swm")
		if err := os.WriteFile(part, payload, 0600); err != nil {
			return nil, err
		}
		return []wim.Part{{Path: part, Size: uint64(len(payload))}}, nil
	}}
	prepared, cleanup, err := splitter.PrepareWithProgress(context.Background(), bytes.NewReader(payload), uint64(len(payload)), hex.EncodeToString(sum[:]), 4096, func() error {
		splittingReported = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatalf("prepared files not retained: %v", err)
	}
	emitted := 0
	if err := prepared.Split(context.Background(), nil, uint64(len(payload)), hex.EncodeToString(sum[:]), 4096, func(part SplitPart) error {
		emitted++
		return nil
	}); err != nil || emitted != 1 {
		t.Fatalf("emit count=%d error=%v", emitted, err)
	}
	if err := cleanup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Close(); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("prepared directory remains: %v", err)
	}
}

func TestNativeWIMPrepareFailureRemovesStagedData(t *testing.T) {
	payload := []byte("corrupt-wim")
	sum := sha256.Sum256(payload)
	var temporary string
	want := errors.New("native parse failed")
	splitter := &NativeWIMSplitter{split: func(_ context.Context, sourcePath, _ string, _ uint64, _ wim.ProgressFunc) ([]wim.Part, error) {
		temporary = filepath.Dir(sourcePath)
		return nil, want
	}}
	_, _, err := splitter.Prepare(context.Background(), bytes.NewReader(payload), uint64(len(payload)), hex.EncodeToString(sum[:]), 4096)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("failed preparation directory remains: %v", err)
	}
}

func TestNativeWIMSplitterRejectsStagedHashMismatchBeforeNativeCall(t *testing.T) {
	payload := []byte("changed")
	splitCalls := 0
	splitter := &NativeWIMSplitter{split: func(context.Context, string, string, uint64, wim.ProgressFunc) ([]wim.Part, error) {
		splitCalls++
		return nil, nil
	}}
	err := splitter.Split(context.Background(), bytes.NewReader(payload), uint64(len(payload)), string(make([]byte, 64)), 1024, func(SplitPart) error { return nil })
	if !errors.Is(err, ErrVerification) || splitCalls != 0 {
		t.Fatalf("error=%v split calls=%d", err, splitCalls)
	}
}

func TestNativeWIMSplitterWaitsForNativeCancellationAndDoesNotEmit(t *testing.T) {
	payload := bytes.Repeat([]byte("wim"), 100)
	sum := sha256.Sum256(payload)
	ctx, cancel := context.WithCancel(context.Background())
	var temporary string
	splitter := &NativeWIMSplitter{split: func(_ context.Context, sourcePath, output string, _ uint64, _ wim.ProgressFunc) ([]wim.Part, error) {
		temporary = filepath.Dir(sourcePath)
		part := filepath.Join(output, "install.swm")
		if err := os.WriteFile(part, payload, 0600); err != nil {
			return nil, err
		}
		cancel() // models cancellation while an uninterruptible native call is returning
		return []wim.Part{{Path: part, Size: uint64(len(payload))}}, nil
	}}
	emitted := false
	err := splitter.Split(ctx, bytes.NewReader(payload), uint64(len(payload)), hex.EncodeToString(sum[:]), 1024, func(SplitPart) error { emitted = true; return nil })
	if !errors.Is(err, context.Canceled) || emitted {
		t.Fatalf("error=%v emitted=%v", err, emitted)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestValidateSplitPartsRejectsInvalidSets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "install2.swm")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSplitParts([]wim.Part{{Path: file, Size: 4}}, dir, 4, 1024); !errors.Is(err, ErrVerification) {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateSplitPartsAcceptsCanonicalizedTemporaryPath(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "temporary-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	output := filepath.Join(aliasRoot, "split")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(output, "install.swm")
	if err := os.WriteFile(part, []byte("valid split payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSplitParts([]wim.Part{{Path: part, Size: 19}}, output, 19, 1024); err != nil {
		t.Fatal(err)
	}
}
