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

	// Setup output format context
	if err := t.setupOutput(); err != nil {
		t.cleanup()
		return fmt.Errorf("setup output: %w", err)
	}

	// Setup encoders
	if err := t.setupEncoders(); err != nil {
		t.cleanup()
		return fmt.Errorf("setup encoders: %w", err)
	}

	// Write header
	status := C.avformat_write_header(t.outputCtx, nil)
	if status < 0 {
		t.cleanup()
		return fmt.Errorf("%d: couldn't write header", status)
	}

	defer t.cleanup()

	// Seek to start position if requested
	if t.startAt > 0 {
		videoStreams := t.input.VideoStreams()
		if len(videoStreams) > 0 {
			if err := videoStreams[0].Rewind(t.startAt); err != nil {
				return fmt.Errorf("seek: %w", err)
			}
		}
	}

	// Main transcoding loop
	var frameCount int64
	startTime := time.Now()

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			C.av_write_trailer(t.outputCtx)
			return ctx.Err()
		default:
		}

		// Read packet from input
		packet, gotPacket, err := t.input.ReadPacket()
		if err != nil {
			if t.onError != nil && !t.onError(err, frameCount) {
				C.av_write_trailer(t.outputCtx)
				return err
			}
			continue
		}
		if !gotPacket {
			break // EOF
		}

		// Check duration limit
		if t.duration > 0 {
			elapsed := time.Since(startTime)
			if elapsed > t.duration {
				break
			}
		}

		// Process based on packet type
		switch packet.Type() {
		case StreamVideo:
			if t.videoEncCtx != nil {
				// For now, just count frames
				// Full decode->filter->encode pipeline needs AVFrame access
				frameCount++
			}
		case StreamAudio:
			if t.audioEncCtx != nil {
				// Audio processing - skip for now
			}
		}

		// Progress callback
		if t.onProgress != nil && frameCount%30 == 0 {
			totalDur, _ := t.input.Duration()
			progress := float64(frameCount) / float64(totalDur.Seconds()*30)
			if progress > 1.0 {
				progress = 1.0
			}
			t.onProgress(TranscodeStats{
				FramesProcessed: frameCount,
				Duration:        time.Since(startTime),
				TotalDuration:   totalDur,
				Progress:        progress,
				FPS:             float64(frameCount) / time.Since(startTime).Seconds(),
			})
		}
	}

	// Write trailer
	C.av_write_trailer(t.outputCtx)

	return nil
}

// setupEncoders creates video and audio encoder contexts
func (t *Transcoder) setupEncoders() error {
	// Setup video encoder if requested
	if t.videoCodec != "" && t.videoCodec != "copy" {
		videoStreams := t.input.VideoStreams()
		if len(videoStreams) == 0 {
			return fmt.Errorf("no video stream in input")
		}
		inVideo := videoStreams[0]

		codec, err := findEncoder(t.videoCodec)
		if err != nil {
			return err
		}

		// Determine output dimensions (may be modified by filter)
		width := inVideo.Width()
		height := inVideo.Height()

		// Get time base from input stream
		tbNum, tbDen := inVideo.TimeBase()
		timeBase := C.AVRational{num: C.int(tbNum), den: C.int(tbDen)}

		t.videoEncCtx, err = createVideoEncoder(
			codec,
			width, height,
			C.AV_PIX_FMT_YUV420P, // common output format
			timeBase,
			1000000, // 1 Mbps default
		)
		if err != nil {
			return fmt.Errorf("create video encoder: %w", err)
		}

		// Add video stream to output
		outStream := C.avformat_new_stream(t.outputCtx, nil)
		if outStream == nil {
			return fmt.Errorf("couldn't create output video stream")
		}
		C.avcodec_parameters_from_context(outStream.codecpar, t.videoEncCtx)
		outStream.time_base = t.videoEncCtx.time_base
	}

	// Setup audio encoder if requested (skip for now if NoAudio or copy)
	// Audio encoding can be added later

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

// processVideoFrame decodes, filters, encodes and writes a video frame
// NOTE: This is a placeholder. Full implementation requires AVFrame access from VideoStream.
func (t *Transcoder) processVideoFrame(videoStream *VideoStream) error {
	// Read decoded frame
	frame, gotFrame, err := videoStream.ReadVideoFrame()
	if err != nil {
		return err
	}
	if !gotFrame || frame == nil {
		return nil // EAGAIN
	}

	// For now, skip actual encoding (would need AVFrame access)
	// Full implementation would:
	// 1. Get raw AVFrame from decoder
	// 2. Push through filter graph
	// 3. Pull filtered frame
	// 4. Encode
	// 5. Write packet to muxer

	return nil
}
