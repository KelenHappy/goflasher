package progress

import "time"

type Stage string

const (
	StageWriting   Stage = "writing"
	StageFlushing  Stage = "flushing"
	StageVerifying Stage = "verifying"
	StageEjecting  Stage = "ejecting"
)

type Update struct {
	Stage          Stage
	BytesProcessed uint64
	TotalBytes     uint64
	BytesPerSecond float64
	ETA            time.Duration
}

// Calculate produces a stable progress snapshot. Unknown totals and speeds
// deliberately yield a zero ETA rather than a misleading estimate.
func Calculate(stage Stage, processed, total uint64, elapsed time.Duration) Update {
	u := Update{Stage: stage, BytesProcessed: processed, TotalBytes: total}
	if elapsed > 0 {
		u.BytesPerSecond = float64(processed) / elapsed.Seconds()
	}
	if total > processed && u.BytesPerSecond > 0 {
		u.ETA = time.Duration(float64(total-processed) / u.BytesPerSecond * float64(time.Second))
	}
	return u
}
