package iso

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestVolumeLabelReadsPrimaryDescriptor(t *testing.T) {
	b := oneFileISO("FILE", false, 30)
	copy(b[16*2048+40:], "FEDORA-WS-LIVE-44                ")
	r, err := New(bytes.NewReader(b), int64(len(b)), nopCloser{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.VolumeLabel(); got != "FEDORA-WS-LIVE-44" {
		t.Fatalf("label=%q", got)
	}
}

func TestVolumeLabelEmptyWithoutPrimaryDescriptor(t *testing.T) {
	r, err := New(bytes.NewReader(oneFileUDF()), int64(len(oneFileUDF())), nopCloser{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.VolumeLabel(); got != "" {
		t.Fatalf("label=%q", got)
	}
}

func TestReadFileStitchesExtents(t *testing.T) {
	image := []byte("....hello....world..")
	r := &Reader{source: bytes.NewReader(image), size: imageSize(len(image))}
	e := Entry{Type: File, Size: 10, Extents: []Extent{{4, 5}, {13, 5}}}
	got, err := r.ReadFile(e, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "helloworld" {
		t.Fatalf("contents=%q", got)
	}
	f, err := r.FileReader(e)
	if err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 4)
	if _, err := f.ReadAt(part, 3); err != nil || string(part) != "lowo" {
		t.Fatalf("ReadAt=%q err=%v", part, err)
	}
	if _, err := f.ReadAt(part, 8); !errors.Is(err, io.EOF) {
		t.Fatalf("read past end err=%v", err)
	}
}

func TestReadFileRejectsUnsafeEntries(t *testing.T) {
	image := make([]byte, 100)
	r := &Reader{source: bytes.NewReader(image), size: imageSize(len(image))}
	for name, e := range map[string]Entry{
		"directory":     {Type: Directory},
		"over limit":    {Type: File, Size: 50, Extents: []Extent{{0, 50}}},
		"out of bounds": {Type: File, Size: 10, Extents: []Extent{{95, 10}}},
		"short extents": {Type: File, Size: 10, Extents: []Extent{{0, 5}}},
	} {
		if _, err := r.ReadFile(e, 20); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestInspectionAcceptsCaseCollisionsInstallerRejects(t *testing.T) {
	entries := []Entry{
		{Path: "EFI/BOOT/BOOTX64.EFI", Type: File, Size: 10, Extents: []Extent{{100, 10}}},
		{Path: "EFI/BOOT/bootx64.efi", Type: File, Size: 10, Extents: []Extent{{105, 10}}},
	}
	if err := (&Reader{size: 1000}).validate(entries); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("installer validation err=%v", err)
	}
	if err := (&Reader{size: 1000, inspection: true}).validate(entries); err != nil {
		t.Fatalf("inspection validation err=%v", err)
	}
	outOfBounds := []Entry{{Path: "a", Type: File, Size: 10, Extents: []Extent{{995, 10}}}}
	if err := (&Reader{size: 1000, inspection: true}).validate(outOfBounds); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("inspection must keep bounds checks, err=%v", err)
	}
}
