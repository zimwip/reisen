package reisen

/*
#cgo pkg-config: libavformat libavcodec libavutil libavfilter
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/opt.h>
*/
import "C"

import (
	"context"
	"io"
	"time"
)

// Ensure imports are used (will be used in later tasks)
var _ context.Context

// TranscodeStats provides progress information during transcoding
type TranscodeStats struct {
	FramesProcessed int64
	Duration        time.Duration
	TotalDuration   time.Duration
	Progress        float64
	FPS             float64
}

// ProgressCallback is called periodically during transcoding
type ProgressCallback func(TranscodeStats)

// ErrorCallback is called when an error occurs during transcoding
// Return true to continue, false to stop
type ErrorCallback func(err error, frameNum int64) bool

// Transcoder configures and runs a transcode job
type Transcoder struct {
	input       *Media
	output      io.WriteSeeker
	videoCodec  string
	audioCodec  string
	videoFilter string
	audioFilter string
	format      string
	formatOpts  map[string]string
	startAt     time.Duration
	duration    time.Duration

	onProgress ProgressCallback
	onError    ErrorCallback
}

// NewTranscoder creates a new transcoder from input Media to output writer
func NewTranscoder(input *Media, output io.WriteSeeker) *Transcoder {
	return &Transcoder{
		input:      input,
		output:     output,
		formatOpts: make(map[string]string),
	}
}

// VideoCodec sets the video encoder (e.g., "libx264", "libvpx")
func (t *Transcoder) VideoCodec(codec string) *Transcoder {
	t.videoCodec = codec
	return t
}

// VideoFilter sets the video filter graph (e.g., "scale=1280:-2")
func (t *Transcoder) VideoFilter(filter string) *Transcoder {
	t.videoFilter = filter
	return t
}

// NoVideo disables video output
func (t *Transcoder) NoVideo() *Transcoder {
	t.videoCodec = ""
	return t
}

// AudioCodec sets the audio encoder (e.g., "aac", "libopus")
func (t *Transcoder) AudioCodec(codec string) *Transcoder {
	t.audioCodec = codec
	return t
}

// AudioPassthrough copies audio without re-encoding
func (t *Transcoder) AudioPassthrough() *Transcoder {
	t.audioCodec = "copy"
	return t
}

// AudioFilter sets the audio filter graph (e.g., "volume=0.5")
func (t *Transcoder) AudioFilter(filter string) *Transcoder {
	t.audioFilter = filter
	return t
}

// NoAudio disables audio output
func (t *Transcoder) NoAudio() *Transcoder {
	t.audioCodec = ""
	return t
}

// Format sets the output container format (e.g., "mp4", "webm")
func (t *Transcoder) Format(format string) *Transcoder {
	t.format = format
	return t
}

// FormatOption sets a format-specific option (e.g., "movflags", "frag_keyframe")
func (t *Transcoder) FormatOption(key, value string) *Transcoder {
	t.formatOpts[key] = value
	return t
}

// StartAt seeks to the given position before transcoding
func (t *Transcoder) StartAt(d time.Duration) *Transcoder {
	t.startAt = d
	return t
}

// Duration limits output to the given duration
func (t *Transcoder) Duration(d time.Duration) *Transcoder {
	t.duration = d
	return t
}

// OnProgress sets the progress callback
func (t *Transcoder) OnProgress(fn ProgressCallback) *Transcoder {
	t.onProgress = fn
	return t
}

// OnError sets the error callback
func (t *Transcoder) OnError(fn ErrorCallback) *Transcoder {
	t.onError = fn
	return t
}
