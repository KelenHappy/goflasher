package privilege

import (
	"encoding/json"
	"testing"
)

func FuzzSessionProtocolDecoder(f *testing.F) {
	f.Add([]byte(`{"version":2,"kind":"read-at","offset":0,"length":512}`))
	f.Add([]byte(`{"version":4294967295,"kind":"write-at","offset":18446744073709551615,"length":4294967295}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var command SessionCommand
		if json.Unmarshal(data, &command) == nil {
			_ = command.Validate(1 << 30)
		}
	})
}
