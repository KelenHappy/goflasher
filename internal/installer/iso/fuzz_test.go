package iso

import (
	"bytes"
	"testing"
)

func FuzzISOManifestParser(f *testing.F) {
	f.Add(make([]byte, 17*2048))
	f.Add(oneFileISO("FILE", false, 30))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		r, err := New(bytes.NewReader(data), int64(len(data)), nopCloser{})
		if err == nil {
			_ = r.Manifest()
			_ = r.Close()
		}
	})
}
