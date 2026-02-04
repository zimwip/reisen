package reisen

import (
	"testing"
	"time"
)

func TestTranscodeStats(t *testing.T) {
	stats := TranscodeStats{
		FramesProcessed: 100,
		Duration:        10 * time.Second,
		TotalDuration:   60 * time.Second,
		Progress:        0.166,
		FPS:             30.0,
	}

	if stats.FramesProcessed != 100 {
		t.Errorf("expected 100 frames, got %d", stats.FramesProcessed)
	}
	if stats.Progress < 0.16 || stats.Progress > 0.17 {
		t.Errorf("unexpected progress: %f", stats.Progress)
	}
}
