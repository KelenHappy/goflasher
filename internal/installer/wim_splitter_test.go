package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/goflasher/goflasher/internal/wim"
)

func payloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// requireMode fails when path lacks the want permission bits. Windows has no
// POSIX permissions, so only existence is checked there.
func requireMode(t *testing.T, path string, want os.FileMode) error {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != want {
		t.Fatalf("%s mode=%o want=%o", path, info.Mode().Perm(), want)
	}
	return nil
}

// verifyStagedSource checks the private staging directory and source file
// handed to the native splitter.
func verifyStagedSource(t *testing.T, sourcePath string, payload []byte) error {
	t.Helper()
	if err := requireMode(t, filepath.Dir(sourcePath), 0700); err != nil {
		return err
	}
	if err := requireMode(t, sourcePath, 0600); err != nil {
		return err
	}
	got, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("staged source differs")
	}
	return nil
}

// writeSplitParts writes chunks[i] to output/names[i] and describes them as parts.
func writeSplitParts(output string, names []string, chunks [][]byte) ([]wim.Part, error) {
	parts := make([]wim.Part, 0, len(names))
	for i, name := range names {
		path := filepath.Join(output, name)
		if err := os.WriteFile(path, chunks[i], 0600); err != nil {
			return nil, err
		}
		parts = append(parts, wim.Part{Path: path, Size: uint64(len(chunks[i]))})
	}
	return parts, nil
}

func requirePartFileSize(t *testing.T, part SplitPart) {
	t.Helper()
	got, err := os.ReadFile(part.Data.(*os.File).Name())
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(got)) != part.Size {
		t.Fatalf("part %s read=%d want=%d", part.Name, len(got), part.Size)
	}
}

func countEmittedParts(t *testing.T, splitter WIMSplitter, request SplitRequest) int {
	t.Helper()
	emitted := 0
	err := splitter.Split(context.Background(), request, func(SplitPart) error {
		emitted++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return emitted
}

// requireIdempotentClose closes cleanup twice; both calls must succeed.
func requireIdempotentClose(t *testing.T, cleanup io.Closer) {
	t.Helper()
	for attempt := 1; attempt <= 2; attempt++ {
		if err := cleanup.Close(); err != nil {
			t.Fatalf("cleanup attempt %d: %v", attempt, err)
		}
	}
}

func requireRemoved(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s remains: %v", what, err)
	}
}

func TestNativeWIMSplitterStagesRetainedStreamAndCleansEverything(t *testing.T) {
	payload := bytes.Repeat([]byte("retained-wim"), 100)
	const partSize = 3800 << 20
	wantNames := []string{"install.swm", "install2.swm"}
	var temporary string
	splitter := &NativeWIMSplitter{split: func(_ context.Context, req wim.Request) ([]wim.Part, error) {
		sourcePath, output, gotPartSize := req.SourcePath, req.OutputDir, req.PartSize
		temporary = filepath.Dir(sourcePath)
		if gotPartSize != partSize {
			t.Fatalf("part size=%d want=%d", gotPartSize, uint64(partSize))
		}
		if err := verifyStagedSource(t, sourcePath, payload); err != nil {
			return nil, err
		}
		return writeSplitParts(output, wantNames, [][]byte{payload[:700], payload[700:]})
	}}
	var names []string
	err := splitter.Split(context.Background(), SplitRequest{Source: bytes.NewReader(payload), SourceSize: uint64(len(payload)), ExpectedSHA256: payloadDigest(payload), PartSize: partSize}, func(part SplitPart) error {
		names = append(names, part.Name)
		requirePartFileSize(t, part)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("names=%v want=%v", names, wantNames)
	}
	requireRemoved(t, temporary, "temporary directory")
}

func TestNativeWIMPrepareCompletesBeforePartsAreConsumed(t *testing.T) {
	payload := bytes.Repeat([]byte("preflight-wim"), 100)
	request := SplitRequest{Source: bytes.NewReader(payload), SourceSize: uint64(len(payload)), ExpectedSHA256: payloadDigest(payload), PartSize: 4096}
	var temporary string
	splittingReported := false
	splitter := &NativeWIMSplitter{split: func(_ context.Context, req wim.Request) ([]wim.Part, error) {
		sourcePath, output := req.SourcePath, req.OutputDir
		if !splittingReported {
			t.Fatal("native split started before splitting progress")
		}
		temporary = filepath.Dir(sourcePath)
		return writeSplitParts(output, []string{"install.swm"}, [][]byte{payload})
	}}
	prepared, cleanup, err := splitter.PrepareWithProgress(context.Background(), request, func() error {
		splittingReported = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatalf("prepared files not retained: %v", err)
	}
	if emitted := countEmittedParts(t, prepared, request); emitted != 1 {
		t.Fatalf("emit count=%d", emitted)
	}
	requireIdempotentClose(t, cleanup)
	requireRemoved(t, temporary, "prepared directory")
}

func TestNativeWIMPrepareFailureRemovesStagedData(t *testing.T) {
	payload := []byte("corrupt-wim")
	sum := sha256.Sum256(payload)
	var temporary string
	want := errors.New("native parse failed")
	splitter := &NativeWIMSplitter{split: func(_ context.Context, req wim.Request) ([]wim.Part, error) {
		temporary = filepath.Dir(req.SourcePath)
		return nil, want
	}}
	_, _, err := splitter.Prepare(context.Background(), SplitRequest{Source: bytes.NewReader(payload), SourceSize: uint64(len(payload)), ExpectedSHA256: hex.EncodeToString(sum[:]), PartSize: 4096})
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
	splitter := &NativeWIMSplitter{split: func(context.Context, wim.Request) ([]wim.Part, error) {
		splitCalls++
		return nil, nil
	}}
	err := splitter.Split(context.Background(), SplitRequest{Source: bytes.NewReader(payload), SourceSize: uint64(len(payload)), ExpectedSHA256: string(make([]byte, 64)), PartSize: 1024}, func(SplitPart) error { return nil })
	if !errors.Is(err, ErrVerification) || splitCalls != 0 {
		t.Fatalf("error=%v split calls=%d", err, splitCalls)
	}
}

func TestNativeWIMSplitterWaitsForNativeCancellationAndDoesNotEmit(t *testing.T) {
	payload := bytes.Repeat([]byte("wim"), 100)
	sum := sha256.Sum256(payload)
	ctx, cancel := context.WithCancel(context.Background())
	var temporary string
	splitter := &NativeWIMSplitter{split: func(_ context.Context, req wim.Request) ([]wim.Part, error) {
		sourcePath, output := req.SourcePath, req.OutputDir
		temporary = filepath.Dir(sourcePath)
		part := filepath.Join(output, "install.swm")
		if err := os.WriteFile(part, payload, 0600); err != nil {
			return nil, err
		}
		cancel() // models cancellation while an uninterruptible native call is returning
		return []wim.Part{{Path: part, Size: uint64(len(payload))}}, nil
	}}
	emitted := false
	err := splitter.Split(ctx, SplitRequest{Source: bytes.NewReader(payload), SourceSize: uint64(len(payload)), ExpectedSHA256: hex.EncodeToString(sum[:]), PartSize: 1024}, func(SplitPart) error { emitted = true; return nil })
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
