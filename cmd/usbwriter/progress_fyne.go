//go:build fyne && (linux || windows || darwin)

package main

import "github.com/goflasher/goflasher/internal/progress"

// installerProgress maps the Windows installer stages onto one bar. The
// installer always verifies its filesystem, independent of the raw-write
// verify option. Staging, splitting, extraction and verification report bytes.
func installerProgress(update progress.Update) float64 {
	ratio := progressRatio(update)
	switch update.Stage {
	case progress.StageStagingWIM:
		return 0.06 + ratio*0.05
	case progress.StageSplittingWIM:
		return 0.11 + ratio*0.04
	case progress.StageExtracting:
		return 0.20 + ratio*0.60
	case progress.StageVerifyingFilesystem:
		return 0.82 + ratio*0.13
	default:
		return installerStageProgress[update.Stage]
	}
}

var installerStageProgress = map[progress.Stage]float64{
	progress.StageInspecting:   0.02,
	progress.StagePlanning:     0.05,
	progress.StagePartitioning: 0.16,
	progress.StageFormatting:   0.18,
	progress.StageFlushing:     0.80,
	progress.StageEjecting:     0.97,
}

func overallProgress(update progress.Update, verify bool) float64 {
	ratio := progressRatio(update)
	if verify {
		return verifyProgress(update.Stage, ratio)
	}
	return writeProgress(update.Stage, ratio)
}

func progressRatio(update progress.Update) float64 {
	if update.TotalBytes == 0 {
		return 0
	}
	return min(float64(update.BytesProcessed)/float64(update.TotalBytes), 1)
}

func verifyProgress(stage progress.Stage, ratio float64) float64 {
	switch stage {
	case progress.StageWriting, progress.StageDecompressWriting:
		return ratio * 0.45
	case progress.StageFlushing:
		return 0.45
	case progress.StageVerifying:
		return 0.45 + ratio*0.50
	case progress.StageEjecting:
		return 0.95
	default:
		return 0
	}
}

func writeProgress(stage progress.Stage, ratio float64) float64 {
	switch stage {
	case progress.StageFormatting:
		return ratio
	case progress.StageExtracting:
		return 0.30 + ratio*0.60
	case progress.StageWriting, progress.StageDecompressWriting:
		return ratio * 0.90
	default:
		return writeStageProgress[stage]
	}
}

var writeStageProgress = map[progress.Stage]float64{
	progress.StageInspecting:          0.02,
	progress.StagePlanning:            0.05,
	progress.StageStagingWIM:          0.10,
	progress.StageSplittingWIM:        0.10,
	progress.StagePartitioning:        0.15,
	progress.StageFlushing:            0.90,
	progress.StageVerifyingFilesystem: 0.94,
	progress.StageEjecting:            0.95,
}
