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
var (
	_ context.Context
	_ io.WriteSeeker
)

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
