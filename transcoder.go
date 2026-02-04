package reisen

/*
#cgo pkg-config: libavformat libavcodec libavutil libavfilter
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavcodec/packet.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libavutil/avutil.h>
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
	input           io.ReadSeeker
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
	media       *Media // created from input ReadSeeker
	outputCtx   *C.AVFormatContext
	outputIO    *C.AVIOContext
	outputIOBuf unsafe.Pointer
	writerID    uintptr

	videoEncCtx    *C.AVCodecContext
	audioEncCtx    *C.AVCodecContext
	videoFilterCtx *filterContext
	audioFilterCtx *filterContext
}

// NewTranscoder creates a new transcoder from input ReadSeeker to output WriteSeeker
func NewTranscoder(input io.ReadSeeker, output io.WriteSeeker) *Transcoder {
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
		return fmt.Errorf("input reader is nil")
	}
	if t.output == nil {
		return fmt.Errorf("output writer is nil")
	}

	// Create Media from input ReadSeeker
	var err error
	t.media, err = NewMediaFromReader(t.input)
	if err != nil {
		return fmt.Errorf("create media from reader: %w", err)
	}

	// Open for decoding
	if err := t.media.OpenDecode(); err != nil {
		t.cleanup()
		return fmt.Errorf("open decode: %w", err)
	}

	// Open video stream for decoding
	var videoStream *VideoStream
	videoStreams := t.media.VideoStreams()
	if len(videoStreams) > 0 && t.videoCodec != "" {
		videoStream = videoStreams[0]
		if err := videoStream.Open(); err != nil {
			t.cleanup()
			return fmt.Errorf("open video stream: %w", err)
		}
	}

	// Setup output format context
	if err := t.setupOutput(); err != nil {
		t.cleanup()
		return fmt.Errorf("setup output: %w", err)
	}

	// Setup encoders (after video stream is open so we have decoder context)
	if err := t.setupEncoders(videoStream); err != nil {
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
	if t.startAt > 0 && videoStream != nil {
		if err := videoStream.Rewind(t.startAt); err != nil {
			return fmt.Errorf("seek: %w", err)
		}
	}

	// Allocate encoding packet and filtered frame
	encPacket := C.av_packet_alloc()
	if encPacket == nil {
		return fmt.Errorf("couldn't allocate encoding packet")
	}
	defer C.av_packet_free(&encPacket)

	filteredFrame := C.av_frame_alloc()
	if filteredFrame == nil {
		return fmt.Errorf("couldn't allocate filtered frame")
	}
	defer C.av_frame_free(&filteredFrame)

	// Main transcoding loop
	var frameCount int64
	var pts int64
	startTime := time.Now()

	// Calculate duration limit in PTS units
	var maxPts int64 = -1
	if t.duration > 0 && videoStream != nil {
		tbNum, tbDen := videoStream.TimeBase()
		factor := float64(tbDen) / float64(tbNum)
		maxPts = int64(t.duration.Seconds() * factor)
	}

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			t.flushEncoder(encPacket)
			C.av_write_trailer(t.outputCtx)
			return ctx.Err()
		default:
		}

		// Read packet from input
		packet, gotPacket, err := t.media.ReadPacket()
		if err != nil {
			if t.onError != nil && !t.onError(err, frameCount) {
				t.flushEncoder(encPacket)
				C.av_write_trailer(t.outputCtx)
				return err
			}
			continue
		}
		if !gotPacket {
			break // EOF
		}

		// Process based on packet type
		switch packet.Type() {
		case StreamVideo:
			if t.videoEncCtx != nil && videoStream != nil {
				// Decode frame
				_, gotFrame, err := videoStream.ReadVideoFrame()
				if err != nil {
					if t.onError != nil && !t.onError(err, frameCount) {
						continue
					}
				}
				if !gotFrame {
					continue
				}

				rawFrame := videoStream.RawFrame()
				if rawFrame == nil {
					continue
				}

				// Check duration limit by PTS
				if maxPts > 0 && int64(rawFrame.pts) > maxPts {
					goto done
				}

				// Push frame to filter graph
				if t.videoFilterCtx != nil {
					status := C.av_buffersrc_add_frame_flags(
						t.videoFilterCtx.bufferSrc, rawFrame, C.AV_BUFFERSRC_FLAG_KEEP_REF)
					if status < 0 {
						continue
					}

					// Pull filtered frames
					for {
						status = C.av_buffersink_get_frame(t.videoFilterCtx.bufferSink, filteredFrame)
						if status < 0 {
							break // EAGAIN or error
						}

						// Encode filtered frame
						filteredFrame.pict_type = C.AV_PICTURE_TYPE_NONE
						filteredFrame.pts = C.int64_t(pts)
						pts++

						if err := t.encodeAndWrite(filteredFrame, encPacket, 0); err != nil {
							if t.onError != nil && !t.onError(err, frameCount) {
								C.av_frame_unref(filteredFrame)
								continue
							}
						}
						frameCount++
						C.av_frame_unref(filteredFrame)
					}
				} else {
					// No filter - encode directly
					rawFrame.pict_type = C.AV_PICTURE_TYPE_NONE
					rawFrame.pts = C.int64_t(pts)
					pts++

					if err := t.encodeAndWrite(rawFrame, encPacket, 0); err != nil {
						if t.onError != nil && !t.onError(err, frameCount) {
							continue
						}
					}
					frameCount++
				}
			}
		case StreamAudio:
			// Audio processing - skip for now (NoAudio is common use case)
		}

		// Progress callback
		if t.onProgress != nil && frameCount%30 == 0 {
			totalDur, _ := t.media.Duration()
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

done:
	// Flush encoder
	t.flushEncoder(encPacket)

	// Write trailer
	C.av_write_trailer(t.outputCtx)

	return nil
}

// encodeAndWrite encodes a frame and writes it to the output
func (t *Transcoder) encodeAndWrite(frame *C.AVFrame, packet *C.AVPacket, streamIndex int) error {
	status := C.avcodec_send_frame(t.videoEncCtx, frame)
	if status < 0 {
		return fmt.Errorf("%d: couldn't send frame to encoder", status)
	}

	for {
		status = C.avcodec_receive_packet(t.videoEncCtx, packet)
		if status == C.int(ErrorAgain) || status == C.int(ErrorEndOfFile) {
			return nil
		}
		if status < 0 {
			return fmt.Errorf("%d: couldn't receive packet from encoder", status)
		}

		packet.stream_index = C.int(streamIndex)

		// Rescale timestamps
		outStream := t.outputCtx.streams
		C.av_packet_rescale_ts(packet, t.videoEncCtx.time_base, (*outStream).time_base)

		status = C.av_interleaved_write_frame(t.outputCtx, packet)
		C.av_packet_unref(packet)
		if status < 0 {
			return fmt.Errorf("%d: couldn't write packet", status)
		}
	}
}

// flushEncoder flushes remaining frames from the encoder
func (t *Transcoder) flushEncoder(packet *C.AVPacket) {
	if t.videoEncCtx == nil {
		return
	}

	// Send NULL to flush
	C.avcodec_send_frame(t.videoEncCtx, nil)

	for {
		status := C.avcodec_receive_packet(t.videoEncCtx, packet)
		if status < 0 {
			break
		}

		packet.stream_index = 0
		outStream := t.outputCtx.streams
		C.av_packet_rescale_ts(packet, t.videoEncCtx.time_base, (*outStream).time_base)
		C.av_interleaved_write_frame(t.outputCtx, packet)
		C.av_packet_unref(packet)
	}
}

// setupEncoders creates video and audio encoder contexts
func (t *Transcoder) setupEncoders(videoStream *VideoStream) error {
	// Setup video encoder if requested
	if t.videoCodec != "" && t.videoCodec != "copy" && videoStream != nil {
		codec, err := findEncoder(t.videoCodec)
		if err != nil {
			return err
		}

		// Get decoder context
		decCtx := videoStream.DecoderContext()
		if decCtx == nil {
			return fmt.Errorf("video stream not opened for decoding")
		}

		// Determine output dimensions - may be modified by filter
		width := videoStream.Width()
		height := videoStream.Height()

		// Parse filter to detect scale changes
		if t.videoFilterSpec != "" {
			// Try to detect scale filter dimensions
			var scaleW, scaleH int
			if n, _ := fmt.Sscanf(t.videoFilterSpec, "scale=%d:%d", &scaleW, &scaleH); n >= 1 {
				width = scaleW
				if scaleH > 0 {
					height = scaleH
				} else if scaleH == -2 {
					// Maintain aspect ratio, round to nearest even number
					aspectRatio := float64(videoStream.Width()) / float64(videoStream.Height())
					height = (int(float64(width)/aspectRatio) + 1) / 2 * 2
				}
			}
		}

		// Get frame rate for time base
		frNum, frDen := videoStream.FrameRate()
		timeBase := C.AVRational{num: C.int(frDen), den: C.int(frNum)}

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

		// Setup filter graph if filter specified
		if t.videoFilterSpec != "" {
			// Build filter spec with output format conversion
			filterSpec := buildFilterSpec(t.videoFilterSpec, "yuv420p")

			tbNum, tbDen := videoStream.TimeBase()
			inputTimeBase := C.AVRational{num: C.int(tbNum), den: C.int(tbDen)}

			t.videoFilterCtx, err = initVideoFilterGraph(decCtx, t.videoEncCtx, inputTimeBase, filterSpec)
			if err != nil {
				return fmt.Errorf("init video filter: %w", err)
			}
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

	// Close media (created from input ReadSeeker)
	if t.media != nil {
		t.media.CloseDecode()
		t.media.Close()
		t.media = nil
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
