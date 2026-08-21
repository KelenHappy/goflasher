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
		_, _ = VerifyInstaller(context.Background(), InstallerRequest{Reader: bytes.NewReader(data), TargetSize: uint64(len(data)), Options: InstallerOptions{SplitWIMPolicySize: 3800 << 20}})
	})
}
