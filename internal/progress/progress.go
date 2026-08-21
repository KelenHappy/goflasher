package progress

import "time"

// Stage identifies the operation represented by an Update.
type Stage string

const (
	StageInspecting   Stage = "inspecting"
	StagePlanning     Stage = "planning"
	StageStagingWIM   Stage = "staging_wim"
	StageSplittingWIM Stage = "splitting_wim"
	StagePartitioning Stage = "partitioning"
	// StageFormatting reports creation of a new filesystem.
	StageFormatting Stage = "formatting"
	StageExtracting Stage = "extracting"
	// StageWriting reports an uncompressed image write.
	StageWriting Stage = "writing"
	// StageDecompressWriting reports decompression directly into the target.
	StageDecompressWriting Stage = "decompress_writing"
	// StageFlushing reports that buffered writes are becoming durable.
	StageFlushing Stage = "flushing"
	// StageVerifying reports a target read-back pass.
	StageVerifying           Stage = "verifying"
	StageVerifyingFilesystem Stage = "verifying_filesystem"
	// StageEjecting reports release of the target for safe removal.
	StageEjecting Stage = "ejecting"
)

// Update represents progress at a point in time. Channel ownership is defined
// by the API sending updates; producers in this repository do not close
// caller-owned channels.
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
