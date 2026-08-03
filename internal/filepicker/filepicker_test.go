package filepicker

import (
	"path/filepath"
	"testing"
)

func TestLocalPath(t *testing.T) {
	tests := []struct {
		name  string
		uris  []string
		want  string
		isErr bool
	}{
		{name: "cancelled", uris: nil},
		{name: "local file", uris: []string{"file:///home/user/My%20Image.iso"}, want: filepath.FromSlash("/home/user/My Image.iso")},
		{name: "localhost", uris: []string{"file://localhost/tmp/image.img"}, want: filepath.FromSlash("/tmp/image.img")},
		{name: "remote file", uris: []string{"file://server/image.iso"}, isErr: true},
		{name: "non-file URI", uris: []string{"https://example.com/image.iso"}, isErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := localPath(tt.uris)
			if (err != nil) != tt.isErr {
				t.Fatalf("localPath() error = %v, want error %v", err, tt.isErr)
			}
			if got != tt.want {
				t.Fatalf("localPath() = %q, want %q", got, tt.want)
			}
		})
	}
}
