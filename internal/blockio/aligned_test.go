package blockio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
)

// strictDevice behaves like a Windows raw disk handle: any request whose
// offset or length is not a whole number of sectors fails.
type strictDevice struct {
	data        []byte
	sector      int64
	maxRequest  int
	failReadAt  int64
	failWriteAt int64
	truncateAt  int64
}

var errMisaligned = errors.New("misaligned request")

func newStrictDevice(size, sector int64) *strictDevice {
	return &strictDevice{data: make([]byte, size), sector: sector, failReadAt: -1, failWriteAt: -1, truncateAt: -1}
}

func (d *strictDevice) check(p []byte, off int64) error {
	if off%d.sector != 0 || int64(len(p))%d.sector != 0 {
		return fmt.Errorf("%w: off=%d len=%d", errMisaligned, off, len(p))
	}
	if off < 0 || off+int64(len(p)) > int64(len(d.data)) {
		return fmt.Errorf("request beyond device: off=%d len=%d", off, len(p))
	}
	d.maxRequest = max(d.maxRequest, len(p))
	return nil
}

func (d *strictDevice) ReadAt(p []byte, off int64) (int, error) {
	if err := d.check(p, off); err != nil {
		return 0, err
	}
	if d.failReadAt >= 0 && off <= d.failReadAt && d.failReadAt < off+int64(len(p)) {
		return 0, errors.New("injected read failure")
	}
	if d.truncateAt >= 0 && off+int64(len(p)) > d.truncateAt {
		n := copy(p, d.data[off:max(off, d.truncateAt)])
		return n, io.EOF
	}
	return copy(p, d.data[off:]), nil
}

func (d *strictDevice) WriteAt(p []byte, off int64) (int, error) {
	if err := d.check(p, off); err != nil {
		return 0, err
	}
	if d.failWriteAt >= 0 && off <= d.failWriteAt && d.failWriteAt < off+int64(len(p)) {
		return 0, errors.New("injected write failure")
	}
	return copy(d.data[off:], p), nil
}

func newTestAligned(t *testing.T, size, sector int64) (*Aligned, *strictDevice) {
	t.Helper()
	dev := newStrictDevice(size, sector)
	a, err := NewAligned(dev, uint32(sector), uint64(size))
	if err != nil {
		t.Fatal(err)
	}
	return a, dev
}

func fill(p []byte, seed byte) []byte {
	for i := range p {
		p[i] = seed + byte(i*7)
	}
	return p
}

func TestNewAlignedRejectsInvalidGeometry(t *testing.T) {
	dev := newStrictDevice(4096, 512)
	tests := []struct {
		name   string
		dev    Device
		sector uint32
		size   uint64
	}{
		{"nil device", nil, 512, 4096},
		{"zero sector", dev, 0, 4096},
		{"small sector", dev, 256, 4096},
		{"non power of two", dev, 1536, 4608},
		{"huge sector", dev, 128 << 10, 128 << 10},
		{"zero size", dev, 512, 0},
		{"partial final sector", dev, 512, 4097},
		{"size overflows int64", dev, 512, 1 << 63},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAligned(tt.dev, tt.sector, tt.size); !errors.Is(err, ErrInvalidGeometry) {
				t.Fatalf("error = %v, want ErrInvalidGeometry", err)
			}
		})
	}
}

func TestUnalignedWritePreservesNeighbouringBytes(t *testing.T) {
	a, dev := newTestAligned(t, 8*512, 512)
	fill(dev.data, 1)
	want := append([]byte(nil), dev.data...)
	payload := fill(make([]byte, 1000), 90)
	const off = 300
	copy(want[off:], payload)

	n, err := a.WriteAt(payload, off)
	if err != nil || n != len(payload) {
		t.Fatalf("WriteAt = %d, %v", n, err)
	}
	if !bytes.Equal(dev.data, want) {
		t.Fatal("device contents differ from expected merge")
	}
}

func TestWriteWithinOneSector(t *testing.T) {
	a, dev := newTestAligned(t, 4*512, 512)
	fill(dev.data, 3)
	want := append([]byte(nil), dev.data...)
	copy(want[515:], []byte("abc"))
	if n, err := a.WriteAt([]byte("abc"), 515); err != nil || n != 3 {
		t.Fatalf("WriteAt = %d, %v", n, err)
	}
	if !bytes.Equal(dev.data, want) {
		t.Fatal("single-sector patch changed unrelated bytes")
	}
}

func TestAlignedWriteSkipsReadModifyWrite(t *testing.T) {
	a, dev := newTestAligned(t, 8*512, 512)
	dev.failReadAt = 0 // any read of the first sectors would fail
	payload := fill(make([]byte, 1024), 5)
	if n, err := a.WriteAt(payload, 0); err != nil || n != len(payload) {
		t.Fatalf("WriteAt = %d, %v", n, err)
	}
	if !bytes.Equal(dev.data[:1024], payload) {
		t.Fatal("aligned write mismatch")
	}
}

func TestUnalignedReadReturnsExactRange(t *testing.T) {
	a, dev := newTestAligned(t, 16*512, 512)
	fill(dev.data, 7)
	got := make([]byte, 1777)
	if n, err := a.ReadAt(got, 333); err != nil || n != len(got) {
		t.Fatalf("ReadAt = %d, %v", n, err)
	}
	if !bytes.Equal(got, dev.data[333:333+1777]) {
		t.Fatal("unaligned read mismatch")
	}
}

func TestReadAtEndReportsEOF(t *testing.T) {
	a, dev := newTestAligned(t, 4*512, 512)
	fill(dev.data, 9)
	got := make([]byte, 100)
	n, err := a.ReadAt(got, 2000)
	if n != 48 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt = %d, %v; want 48, EOF", n, err)
	}
	if !bytes.Equal(got[:n], dev.data[2000:]) {
		t.Fatal("tail read mismatch")
	}
	if n, err := a.ReadAt(got, 2048); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt past end = %d, %v", n, err)
	}
	if n, err := a.ReadAt(got, -1); n != 0 || !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("ReadAt negative = %d, %v", n, err)
	}
}

func TestWriteOutOfRangeIsRejectedBeforeWriting(t *testing.T) {
	a, dev := newTestAligned(t, 4*512, 512)
	before := append([]byte(nil), dev.data...)
	for _, tt := range []struct {
		off int64
		n   int
	}{{-1, 1}, {2047, 2}, {2049, 0}, {0, 2049}} {
		if n, err := a.WriteAt(fill(make([]byte, tt.n), 1), tt.off); n != 0 || !errors.Is(err, ErrOutOfRange) {
			t.Fatalf("WriteAt(len=%d, off=%d) = %d, %v", tt.n, tt.off, n, err)
		}
	}
	if !bytes.Equal(dev.data, before) {
		t.Fatal("rejected write modified the device")
	}
	if n, err := a.WriteAt([]byte{1}, 2047); n != 1 || err != nil {
		t.Fatalf("last-byte write = %d, %v", n, err)
	}
}

func TestDeviceErrorsPropagate(t *testing.T) {
	a, dev := newTestAligned(t, 8*512, 512)
	dev.failReadAt = 1024
	if _, err := a.WriteAt([]byte{1}, 1030); err == nil {
		t.Fatal("read-modify-write ignored a read failure")
	}
	if dev.data[1030] != 0 {
		t.Fatal("failed read-modify-write still wrote")
	}
	if _, err := a.ReadAt(make([]byte, 10), 1025); err == nil {
		t.Fatal("unaligned read ignored a device failure")
	}

	// The head patch (24 bytes) and one whole sector succeed; the tail patch
	// fails, so exactly the committed prefix is reported.
	dev.failReadAt = -1
	dev.failWriteAt = 2048
	n, err := a.WriteAt(make([]byte, 1500), 1000)
	if err == nil || n != 24+1024 {
		t.Fatalf("WriteAt = %d, %v; want %d and an error", n, err, 24+1024)
	}
}

func TestShortDeviceReadIsAnError(t *testing.T) {
	a, dev := newTestAligned(t, 8*512, 512)
	dev.truncateAt = 1536
	if _, err := a.ReadAt(make([]byte, 100), 1500); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want ErrUnexpectedEOF", err)
	}
	if _, err := a.WriteAt([]byte{1}, 1600); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want ErrUnexpectedEOF", err)
	}
}

func TestDeviceRequestsAreBounded(t *testing.T) {
	const size = 4*maxTransfer + 4096
	a, dev := newTestAligned(t, size, 4096)
	payload := fill(make([]byte, 3*maxTransfer+100), 11)
	if _, err := a.WriteAt(payload, 4096+17); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := a.ReadAt(got, 4096+17); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("large round trip mismatch")
	}
	aligned := make([]byte, 2*maxTransfer)
	if _, err := a.ReadAt(aligned, 0); err != nil {
		t.Fatal(err)
	}
	if dev.maxRequest > maxTransfer {
		t.Fatalf("largest device request %d exceeds %d", dev.maxRequest, maxTransfer)
	}
}

// TestMatchesByteModel replays random overlapping reads and writes against a
// plain byte slice and requires identical observable behaviour.
func TestMatchesByteModel(t *testing.T) {
	for _, sector := range []int64{512, 4096} {
		t.Run(fmt.Sprint(sector), func(t *testing.T) {
			const size = 64 * 4096
			a, dev := newTestAligned(t, size, sector)
			model := make([]byte, size)
			rng := rand.New(rand.NewSource(sector))
			for i := 0; i < 2000; i++ {
				off := rng.Int63n(size)
				n := rng.Intn(int(min(size-off, 3*sector+1))) + 1
				if rng.Intn(2) == 0 {
					p := make([]byte, n)
					rng.Read(p)
					if w, err := a.WriteAt(p, off); err != nil || w != n {
						t.Fatalf("op %d WriteAt(%d, %d) = %d, %v", i, n, off, w, err)
					}
					copy(model[off:], p)
					continue
				}
				got := make([]byte, n)
				if r, err := a.ReadAt(got, off); err != nil || r != n {
					t.Fatalf("op %d ReadAt(%d, %d) = %d, %v", i, n, off, r, err)
				}
				if !bytes.Equal(got, model[off:off+int64(n)]) {
					t.Fatalf("op %d read mismatch at %d", i, off)
				}
			}
			if !bytes.Equal(dev.data, model) {
				t.Fatal("final device contents differ from model")
			}
		})
	}
}
