package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
)

type noRawWriteBackend struct{ openWriter, writes, unmounts int }

func (*noRawWriteBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	return nil, nil
}

func TestWindowsParserFailureNeverUsesRawWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.iso")
	if err := os.WriteFile(path, make([]byte, 17*2048), 0600); err != nil {
		t.Fatal(err)
	}
	b := &noRawWriteBackend{}
	_, err := (&Service{Backend: b}).Run(context.Background(), image.Info{Path: path, Format: image.FormatISO, Compression: image.CompressionNone}, device.Device{Size: 1 << 30}, RunOptions{}, nil)
	if err == nil {
		t.Fatal("Run() succeeded")
	}
	if b.openWriter != 0 || b.writes != 0 {
		t.Fatalf("raw path used: %+v", b)
	}
}

func TestCompressedWindowsRejectedAndLinuxHybridRawWritten(t *testing.T) {
	encoders := map[string]func([]byte) []byte{
		"gz": func(p []byte) []byte {
			var b bytes.Buffer
			w := gzip.NewWriter(&b)
			_, _ = w.Write(p)
			_ = w.Close()
			return b.Bytes()
		},
		"xz": func(p []byte) []byte {
			var b bytes.Buffer
			w, _ := xz.NewWriter(&b)
			_, _ = w.Write(p)
			_ = w.Close()
			return b.Bytes()
		},
	}
	for suffix, encode := range encoders {
		t.Run("windows."+suffix, func(t *testing.T) { runCompressedWorkflowTest(t, encode(windowsISOFixture()), suffix, false) })
		t.Run("linux."+suffix, func(t *testing.T) {
			p := linuxHybridISOFixture(t)
			p[446+4] = 0x17
			binary.LittleEndian.PutUint32(p[446+8:], 1)
			binary.LittleEndian.PutUint32(p[446+12:], 159)
			p[510], p[511] = 0x55, 0xaa
			runCompressedWorkflowTest(t, encode(p), suffix, true)
		})
	}
}

func linuxHybridISOFixture(t *testing.T) []byte {
	t.Helper()
	p := windowsISOFixture()
	for old, replacement := range map[string]string{"BOOTMGR": "README1", "BOOT.WIM": "KERNEL01", "INSTALL.ESD": "FILESYSTEM1", "BOOTX64.EFI": "STARTUP.BIN"} {
		i := bytes.Index(p, []byte(old))
		if i < 0 || len(old) != len(replacement) {
			t.Fatalf("invalid fixture replacement %s", old)
		}
		copy(p[i:], replacement)
	}
	root := p[20*2048 : 21*2048]
	off := 0
	for root[off] != 0 {
		off += int(root[off])
	}
	copy(root[off:], appISORecord(24, 2048, []byte(".DISK"), true))
	dir := p[24*2048 : 25*2048]
	off = 0
	for _, rec := range [][]byte{appISORecord(24, 2048, []byte{0}, true), appISORecord(20, 2048, []byte{1}, true), appISORecord(34, 1, []byte("INFO;1"), false)} {
		copy(dir[off:], rec)
		off += len(rec)
	}
	return p
}
func runCompressedWorkflowTest(t *testing.T, data []byte, suffix string, wantRaw bool) {
	path := filepath.Join(t.TempDir(), "source.iso."+suffix)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	b := &noRawWriteBackend{}
	s := &Service{Backend: b, State: readyToRunState(t)}
	_, err = s.Run(context.Background(), info, device.Device{Size: 1 << 30}, RunOptions{}, nil)
	if wantRaw {
		if err != nil {
			t.Fatal(err)
		}
		if b.openWriter != 1 || b.writes == 0 {
			t.Fatalf("raw path calls: %+v", b)
		}
		return
	}
	if !errors.Is(err, ErrCompressedWindowsInstallerUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if b.openWriter != 0 || b.writes != 0 || b.unmounts != 0 {
		t.Fatalf("raw path used: %+v", b)
	}
}
func (*noRawWriteBackend) RefreshDevice(context.Context, string) (device.Device, error) {
	return device.Device{}, nil
}
func (b *noRawWriteBackend) Unmount(context.Context, device.Device) error { b.unmounts++; return nil }
func (b *noRawWriteBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	b.openWriter++
	return countingWriter{b}, nil
}
func (*noRawWriteBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	return nil, errors.New("unexpected OpenReader")
}
func (*noRawWriteBackend) Flush(context.Context, device.Device) error { return nil }
func (*noRawWriteBackend) Eject(context.Context, device.Device) error { return nil }

type countingWriter struct{ b *noRawWriteBackend }

func (w countingWriter) Write(p []byte) (int, error) { w.b.writes++; return len(p), nil }
func (countingWriter) Close() error                  { return nil }

func TestWindowsInstallerNeverFallsBackToRawWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "innocent-name.iso")
	data := windowsISOFixture()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &noRawWriteBackend{}
	service := Service{Backend: backend}
	_, err := service.Run(context.Background(), image.Info{Path: path, Format: image.FormatISO, Compression: image.CompressionNone}, device.Device{Size: uint64(len(data) * 2)}, RunOptions{}, nil)
	if !errors.Is(err, ErrInstallerBuilderUnavailable) {
		t.Fatalf("Run() error = %v", err)
	}
	if backend.openWriter != 0 || backend.writes != 0 {
		t.Fatalf("raw path used: OpenWriter=%d Write=%d", backend.openWriter, backend.writes)
	}
	if backend.unmounts != 0 {
		t.Fatalf("device unmounted before fail-closed decision: %d", backend.unmounts)
	}
}

func windowsISOFixture() []byte {
	b := make([]byte, 40*2048)
	pvd := b[16*2048 : 17*2048]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	copy(pvd[156:], appISORecord(20, 2048, []byte{0}, true))
	term := b[17*2048 : 18*2048]
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1
	dirs := map[uint32][][]byte{
		20: {appISORecord(20, 2048, []byte{0}, true), appISORecord(20, 2048, []byte{1}, true), appISORecord(21, 2048, []byte("SOURCES"), true), appISORecord(22, 2048, []byte("EFI"), true), appISORecord(30, 1, []byte("BOOTMGR;1"), false)},
		21: {appISORecord(21, 2048, []byte{0}, true), appISORecord(20, 2048, []byte{1}, true), appISORecord(31, 1, []byte("BOOT.WIM;1"), false), appISORecord(32, 1, []byte("INSTALL.ESD;1"), false)},
		22: {appISORecord(22, 2048, []byte{0}, true), appISORecord(20, 2048, []byte{1}, true), appISORecord(23, 2048, []byte("BOOT"), true)},
		23: {appISORecord(23, 2048, []byte{0}, true), appISORecord(22, 2048, []byte{1}, true), appISORecord(33, 1, []byte("BOOTX64.EFI;1"), false)},
	}
	for sector, records := range dirs {
		off := int(sector) * 2048
		for _, rec := range records {
			copy(b[off:], rec)
			off += len(rec)
		}
	}
	return b
}

func appISORecord(extent, size uint32, name []byte, directory bool) []byte {
	n := 33 + len(name)
	if n%2 != 0 {
		n++
	}
	r := make([]byte, n)
	r[0] = byte(n)
	binary.LittleEndian.PutUint32(r[2:], extent)
	binary.BigEndian.PutUint32(r[6:], extent)
	binary.LittleEndian.PutUint32(r[10:], size)
	binary.BigEndian.PutUint32(r[14:], size)
	if directory {
		r[25] = 2
	}
	r[28], r[31], r[32] = 1, 1, byte(len(name))
	copy(r[33:], name)
	return r
}
