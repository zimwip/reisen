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
	"fmt"
	"io"
	"time"
	"unsafe"
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
	// Config fields
	input           *Media
	output          io.WriteSeeker
	videoCodec      string
	audioCodec      string
	videoFilterSpec string
	audioFilterSpec string
	format          string
	formatOpts      map[string]string
	startAt         time.Duration
	duration        time.Duration

	onProgress ProgressCallback
	onError    ErrorCallback

	// Runtime fields (allocated during Run)
	outputCtx   *C.AVFormatContext
	outputIO    *C.AVIOContext
	outputIOBuf unsafe.Pointer
	writerID    uintptr

	videoEncCtx    *C.AVCodecContext
	audioEncCtx    *C.AVCodecContext
	videoFilterCtx *filterContext
	audioFilterCtx *filterContext
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
	t.videoFilterSpec = filter
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
	t.audioFilterSpec = filter
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

// Run executes the transcoding operation
func (t *Transcoder) Run(ctx context.Context) error {
	if t.input == nil {
		return fmt.Errorf("input media is nil")
	}

	if t.output == nil {
		return fmt.Errorf("output writer is nil")
	}

	// Setup output
	if err := t.setupOutput(); err != nil {
		t.cleanup()
		return fmt.Errorf("setup output: %w", err)
	}

	defer t.cleanup()

	// TODO: Setup encoders (Task 9)
	// TODO: Write header
	// TODO: Main loop
	// TODO: Write trailer

	return nil
}

// setupOutput creates the output format context with custom AVIO
func (t *Transcoder) setupOutput() error {
	// Create output AVIO context
	var err error
	t.outputIO, t.writerID, t.outputIOBuf, err = createOutputAVIO(t.output)
	if err != nil {
		return err
	}

	// Determine output format
	formatName := t.format
	if formatName == "" {
		formatName = "mp4" // default
	}

	cFormat := C.CString(formatName)
	defer C.free(unsafe.Pointer(cFormat))

	status := C.avformat_alloc_output_context2(&t.outputCtx, nil, cFormat, nil)
	if status < 0 {
		return fmt.Errorf("%d: couldn't allocate output context", status)
	}

	// Attach custom AVIO
	t.outputCtx.pb = t.outputIO
	t.outputCtx.flags |= C.AVFMT_FLAG_CUSTOM_IO

	// Set format options
	for key, value := range t.formatOpts {
		cKey := C.CString(key)
		cValue := C.CString(value)
		C.av_opt_set(unsafe.Pointer(t.outputCtx.priv_data), cKey, cValue, 0)
		C.free(unsafe.Pointer(cKey))
		C.free(unsafe.Pointer(cValue))
	}

	return nil
}

// cleanup frees all allocated resources
func (t *Transcoder) cleanup() {
	// Free encoder contexts
	if t.videoEncCtx != nil {
		C.avcodec_free_context(&t.videoEncCtx)
		t.videoEncCtx = nil
	}
	if t.audioEncCtx != nil {
		C.avcodec_free_context(&t.audioEncCtx)
		t.audioEncCtx = nil
	}

	// Free filter contexts
	if t.videoFilterCtx != nil {
		t.videoFilterCtx.close()
		t.videoFilterCtx = nil
	}
	if t.audioFilterCtx != nil {
		t.audioFilterCtx.close()
		t.audioFilterCtx = nil
	}

	// Free output format context
	if t.outputCtx != nil {
		C.avformat_free_context(t.outputCtx)
		t.outputCtx = nil
	}

	// Free output AVIO
	if t.outputIO != nil {
		if t.outputIO.buffer != nil {
			C.av_free(unsafe.Pointer(t.outputIO.buffer))
		}
		C.avio_context_free(&t.outputIO)
		t.outputIO = nil
	}
	t.outputIOBuf = nil

	// Unregister writer
	if t.writerID != 0 {
		unregisterWriter(t.writerID)
		t.writerID = 0
	}
}
