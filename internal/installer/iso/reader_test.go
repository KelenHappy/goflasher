package iso

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func TestCorpusISO9660AndJoliet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		joliet   bool
		filename string
	}{{"iso9660", false, "BOOTMGR;1"}, {"joliet", true, "BΩOT"}} {
		t.Run(tc.name, func(t *testing.T) {
			b := oneFileISO(tc.filename, tc.joliet, 30)
			r, err := New(bytes.NewReader(b), int64(len(b)), nopCloser{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			m := r.Manifest()
			if len(m.Entries) != 1 || m.Entries[0].Type != File || m.Entries[0].Extents[0].Offset != 30*2048 {
				t.Fatalf("manifest=%+v", m)
			}
		})
	}
}
func TestCorpusUDFOnly(t *testing.T) {
	b := oneFileUDF()
	r, err := New(bytes.NewReader(b), int64(len(b)), nopCloser{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := r.Manifest()
	if len(m.Entries) != 1 || m.Entries[0].Path != "BOOTMGR" {
		t.Fatalf("manifest=%+v", m)
	}
}
func TestCorpusRejectsMalformedImages(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{{"truncated", make([]byte, 17*2048)}, {"malicious path", oneFileISO("..", false, 30)}, {"out of bounds extent", oneFileISO("FILE", false, 9999)}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(bytes.NewReader(tc.data), int64(len(tc.data)), nopCloser{}); !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
func TestCorpusRejectsPathAndExtentCollisions(t *testing.T) {
	r := &Reader{size: 1000}
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"path collision", []Entry{{Path: "Boot", Type: File}, {Path: "boot", Type: File}}}, {"extent collision", []Entry{{Path: "a", Type: File, Size: 10, Extents: []Extent{{0, 10}}}, {Path: "b", Type: File, Size: 10, Extents: []Extent{{5, 10}}}}}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.validate(tc.entries); !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func oneFileISO(name string, joliet bool, extent uint32) []byte {
	b := make([]byte, 40*2048)
	v := b[16*2048 : 17*2048]
	v[0] = 1
	if joliet {
		v[0] = 2
		v[88], v[89], v[90] = 0x25, 0x2f, 0x45
	}
	copy(v[1:], "CD001")
	v[6] = 1
	copy(v[156:], testRecord(20, 2048, []byte{0}, true))
	term := b[17*2048 : 18*2048]
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1
	off := 20 * 2048
	for _, x := range [][]byte{testRecord(20, 2048, []byte{0}, true), testRecord(20, 2048, []byte{1}, true), testRecord(extent, 1, encodeTestName(name, joliet), false)} {
		copy(b[off:], x)
		off += len(x)
	}
	return b
}
func encodeTestName(s string, j bool) []byte {
	if !j {
		return []byte(s)
	}
	var b []byte
	for _, x := range []rune(s) {
		b = append(b, byte(x>>8), byte(x))
	}
	return b
}
func testRecord(ext, size uint32, name []byte, dir bool) []byte {
	n := 33 + len(name)
	if n%2 != 0 {
		n++
	}
	r := make([]byte, n)
	r[0] = byte(n)
	binary.LittleEndian.PutUint32(r[2:], ext)
	binary.BigEndian.PutUint32(r[6:], ext)
	binary.LittleEndian.PutUint32(r[10:], size)
	binary.BigEndian.PutUint32(r[14:], size)
	if dir {
		r[25] = 2
	}
	r[28], r[31], r[32] = 1, 1, byte(len(name))
	copy(r[33:], name)
	return r
}

func oneFileUDF() []byte {
	b := make([]byte, 300*2048)
	tag := func(sec int, id uint16) []byte {
		x := b[sec*2048 : (sec+1)*2048]
		binary.LittleEndian.PutUint16(x, id)
		return x
	}
	a := tag(256, 2)
	binary.LittleEndian.PutUint32(a[16:], 3*2048)
	binary.LittleEndian.PutUint32(a[20:], 257)
	pd := tag(257, 5)
	binary.LittleEndian.PutUint32(pd[188:], 270)
	lvd := tag(258, 6)
	binary.LittleEndian.PutUint32(lvd[212:], 2048)
	binary.LittleEndian.PutUint32(lvd[252:], 0)
	tag(259, 8)
	fsd := tag(270, 256)
	binary.LittleEndian.PutUint32(fsd[404:], 1)
	name := append([]byte{8}, []byte("BOOTMGR")...)
	fidLen := (38 + len(name) + 3) &^ 3
	root := tag(271, 261)
	root[27] = 4
	binary.LittleEndian.PutUint64(root[56:], uint64(fidLen))
	binary.LittleEndian.PutUint32(root[172:], 8)
	binary.LittleEndian.PutUint32(root[176:], uint32(fidLen))
	binary.LittleEndian.PutUint32(root[180:], 2)
	d := tag(272, 257)
	d[19] = byte(len(name))
	binary.LittleEndian.PutUint32(d[24:], 3)
	copy(d[38:], name)
	file := tag(273, 261)
	file[27] = 5
	binary.LittleEndian.PutUint64(file[56:], 1)
	binary.LittleEndian.PutUint32(file[172:], 8)
	binary.LittleEndian.PutUint32(file[176:], 1)
	binary.LittleEndian.PutUint32(file[180:], 4)
	b[274*2048] = 'x'
	return b
}

var _ io.Closer = nopCloser{}
