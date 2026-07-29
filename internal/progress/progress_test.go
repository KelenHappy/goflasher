package progress

import (
	"testing"
	"time"
)

func TestCalculate(t *testing.T) {
	u := Calculate(StageWriting, 250, 1000, 2*time.Second)
	if u.BytesPerSecond != 125 {
		t.Fatalf("speed = %v", u.BytesPerSecond)
	}
	if u.ETA != 6*time.Second {
		t.Fatalf("ETA = %v", u.ETA)
	}
	if got := Calculate(StageWriting, 0, 0, 0); got.ETA != 0 || got.BytesPerSecond != 0 {
		t.Fatalf("unknown progress = %+v", got)
	}
}
