package reisen

import (
	"bytes"
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

func TestTranscoderBuilder(t *testing.T) {
	// We can't create a real Media without a file, so test nil handling
	var output bytes.Buffer
	ws := &bufferWriteSeeker{buf: &output}

	tr := NewTranscoder(nil, ws).
		VideoCodec("libx264").
		VideoFilter("scale=1280:-2").
		AudioCodec("aac").
		AudioFilter("volume=0.5").
		Format("mp4").
		FormatOption("movflags", "frag_keyframe").
		StartAt(10 * time.Second).
		Duration(60 * time.Second)

	if tr.videoCodec != "libx264" {
		t.Errorf("expected libx264, got %s", tr.videoCodec)
	}
	if tr.videoFilter != "scale=1280:-2" {
		t.Errorf("expected scale filter, got %s", tr.videoFilter)
	}
	if tr.audioCodec != "aac" {
		t.Errorf("expected aac, got %s", tr.audioCodec)
	}
	if tr.format != "mp4" {
		t.Errorf("expected mp4, got %s", tr.format)
	}
	if tr.formatOpts["movflags"] != "frag_keyframe" {
		t.Errorf("expected movflags option")
	}
	if tr.startAt != 10*time.Second {
		t.Errorf("expected 10s start, got %v", tr.startAt)
	}
	if tr.duration != 60*time.Second {
		t.Errorf("expected 60s duration, got %v", tr.duration)
	}
}

func TestTranscoderNoVideo(t *testing.T) {
	var output bytes.Buffer
	ws := &bufferWriteSeeker{buf: &output}

	tr := NewTranscoder(nil, ws).NoVideo()

	if tr.videoCodec != "" {
		t.Errorf("expected empty video codec for NoVideo")
	}
}

func TestTranscoderNoAudio(t *testing.T) {
	var output bytes.Buffer
	ws := &bufferWriteSeeker{buf: &output}

	tr := NewTranscoder(nil, ws).NoAudio()

	if tr.audioCodec != "" {
		t.Errorf("expected empty audio codec for NoAudio")
	}
}

func TestTranscoderAudioPassthrough(t *testing.T) {
	var output bytes.Buffer
	ws := &bufferWriteSeeker{buf: &output}

	tr := NewTranscoder(nil, ws).AudioPassthrough()

	if tr.audioCodec != "copy" {
		t.Errorf("expected 'copy' for passthrough, got %s", tr.audioCodec)
	}
}
