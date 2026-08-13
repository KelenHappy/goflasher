package progress

import "time"

// Stage identifies the operation represented by an Update.
type Stage string

const (
	// StageFormatting reports creation of a new filesystem.
	StageFormatting Stage = "formatting"
	// StageWriting reports an uncompressed image write.
	StageWriting Stage = "writing"
	// StageDecompressWriting reports decompression directly into the target.
	StageDecompressWriting Stage = "decompress_writing"
	// StageFlushing reports that buffered writes are becoming durable.
	StageFlushing Stage = "flushing"
	// StageVerifying reports a target read-back pass.
	StageVerifying Stage = "verifying"
	// StageEjecting reports release of the target for safe removal.
	StageEjecting Stage = "ejecting"
)

// Update is an immutable progress snapshot passed from a worker to its owner.
// Producers do not close the channel carrying updates; the goroutine that
// created that channel owns its lifecycle.
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
