package verify

import (
	"bytes"
	"context"
	"testing"
)

func FuzzGPTAndFAT32Verifier(f *testing.F) {
	f.Add(make([]byte, 34*512))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = VerifyInstaller(context.Background(), bytes.NewReader(data), uint64(len(data)), nil, InstallerOptions{SplitWIMPolicySize: 3800 << 20})
	})
}
