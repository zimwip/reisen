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

	// Stream tracking
	videoStream     *VideoStream
	audioStream     *AudioStream
	audioStreamIdx  int // output stream index for audio
	videoStreamIdx  int // output stream index for video
	audioPassthrough bool // true if copying audio without re-encoding
	videoPassthrough bool // true if copying video without re-encoding
	needsFaststart   bool // true if we handle moov relocation after writing
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

// VideoPassthrough copies video without re-encoding
func (t *Transcoder) VideoPassthrough() *Transcoder {
	t.videoCodec = "copy"
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
	videoStreams := t.media.VideoStreams()
	if len(videoStreams) > 0 && t.videoCodec != "" {
		t.videoStream = videoStreams[0]
		// Only open for decoding if we need to re-encode (not passthrough)
		if t.videoCodec != "copy" {
			if err := t.videoStream.Open(); err != nil {
				t.cleanup()
				return fmt.Errorf("open video stream: %w", err)
			}
		}
	}

	// Open audio stream for decoding (unless passthrough or disabled)
	audioStreams := t.media.AudioStreams()
	if len(audioStreams) > 0 && t.audioCodec != "" {
		t.audioStream = audioStreams[0]
		// Only open for decoding if we need to re-encode (not passthrough)
		if t.audioCodec != "copy" {
			if err := t.audioStream.Open(); err != nil {
				t.cleanup()
				return fmt.Errorf("open audio stream: %w", err)
			}
		}
	}

	// Setup output format context
	if err := t.setupOutput(); err != nil {
		t.cleanup()
		return fmt.Errorf("setup output: %w", err)
	}

	// Setup encoders (after streams are open so we have decoder contexts)
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
	if t.startAt > 0 && t.videoStream != nil {
		if err := t.videoStream.Rewind(t.startAt); err != nil {
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
	var videoPts int64
	var audioPts int64
	startTime := time.Now()

	// Calculate duration limit in PTS units
	var maxPts int64 = -1
	if t.duration > 0 && t.videoStream != nil {
		tbNum, tbDen := t.videoStream.TimeBase()
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
			if t.videoPassthrough && t.videoStream != nil {
				// Passthrough mode - copy packet directly
				if err := t.writeVideoPassthrough(packet); err != nil {
					if t.onError != nil && !t.onError(err, frameCount) {
						continue
					}
				}
			} else if t.videoEncCtx != nil && t.videoStream != nil {
				// Decode frame
				_, gotFrame, err := t.videoStream.ReadVideoFrame()
				if err != nil {
					if t.onError != nil && !t.onError(err, frameCount) {
						continue
					}
				}
				if !gotFrame {
					continue
				}

				rawFrame := t.videoStream.RawFrame()
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
						filteredFrame.pts = C.int64_t(videoPts)
						videoPts++

						if err := t.encodeAndWriteVideo(filteredFrame, encPacket); err != nil {
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
					rawFrame.pts = C.int64_t(videoPts)
					videoPts++

					if err := t.encodeAndWriteVideo(rawFrame, encPacket); err != nil {
						if t.onError != nil && !t.onError(err, frameCount) {
							continue
						}
					}
					frameCount++
				}
			}
		case StreamAudio:
			if t.audioStream != nil {
				if t.audioPassthrough {
					// Passthrough mode - copy packet directly
					if err := t.writeAudioPassthrough(packet); err != nil {
						if t.onError != nil && !t.onError(err, frameCount) {
							continue
						}
					}
				} else if t.audioEncCtx != nil {
					// Decode audio frame
					_, gotFrame, err := t.audioStream.ReadAudioFrame()
					if err != nil {
						if t.onError != nil && !t.onError(err, frameCount) {
							continue
						}
					}
					if !gotFrame {
						continue
					}

					rawFrame := t.audioStream.RawFrame()
					if rawFrame == nil {
						continue
					}

					// Push frame to filter graph if configured
					if t.audioFilterCtx != nil {
						status := C.av_buffersrc_add_frame_flags(
							t.audioFilterCtx.bufferSrc, rawFrame, C.AV_BUFFERSRC_FLAG_KEEP_REF)
						if status < 0 {
							continue
						}

						// Pull filtered frames
						for {
							status = C.av_buffersink_get_frame(t.audioFilterCtx.bufferSink, filteredFrame)
							if status < 0 {
								break // EAGAIN or error
							}

							filteredFrame.pts = C.int64_t(audioPts)
							audioPts += int64(filteredFrame.nb_samples)

							if err := t.encodeAndWriteAudio(filteredFrame, encPacket); err != nil {
								if t.onError != nil && !t.onError(err, frameCount) {
									C.av_frame_unref(filteredFrame)
									continue
								}
							}
							C.av_frame_unref(filteredFrame)
						}
					} else {
						// No filter - encode directly
						rawFrame.pts = C.int64_t(audioPts)
						audioPts += int64(rawFrame.nb_samples)

						if err := t.encodeAndWriteAudio(rawFrame, encPacket); err != nil {
							if t.onError != nil && !t.onError(err, frameCount) {
								continue
							}
						}
					}
				}
			}
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

	// Faststart: relocate moov atom before mdat
	if t.needsFaststart {
		if err := t.doFaststart(); err != nil {
			return fmt.Errorf("faststart: %w", err)
		}
	}

	return nil
}

// encodeAndWriteVideo encodes a video frame and writes it to the output
func (t *Transcoder) encodeAndWriteVideo(frame *C.AVFrame, packet *C.AVPacket) error {
	status := C.avcodec_send_frame(t.videoEncCtx, frame)
	if status < 0 {
		return fmt.Errorf("%d: couldn't send video frame to encoder", status)
	}

	for {
		status = C.avcodec_receive_packet(t.videoEncCtx, packet)
		if status == C.int(ErrorAgain) || status == C.int(ErrorEndOfFile) {
			return nil
		}
		if status < 0 {
			return fmt.Errorf("%d: couldn't receive video packet from encoder", status)
		}

		packet.stream_index = C.int(t.videoStreamIdx)

		// Rescale timestamps
		outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
		C.av_packet_rescale_ts(packet, t.videoEncCtx.time_base, outStreams[t.videoStreamIdx].time_base)

		status = C.av_interleaved_write_frame(t.outputCtx, packet)
		C.av_packet_unref(packet)
		if status < 0 {
			return fmt.Errorf("%d: couldn't write video packet", status)
		}
	}
}

// encodeAndWriteAudio encodes an audio frame and writes it to the output
func (t *Transcoder) encodeAndWriteAudio(frame *C.AVFrame, packet *C.AVPacket) error {
	status := C.avcodec_send_frame(t.audioEncCtx, frame)
	if status < 0 {
		return fmt.Errorf("%d: couldn't send audio frame to encoder", status)
	}

	for {
		status = C.avcodec_receive_packet(t.audioEncCtx, packet)
		if status == C.int(ErrorAgain) || status == C.int(ErrorEndOfFile) {
			return nil
		}
		if status < 0 {
			return fmt.Errorf("%d: couldn't receive audio packet from encoder", status)
		}

		packet.stream_index = C.int(t.audioStreamIdx)

		// Rescale timestamps
		outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
		C.av_packet_rescale_ts(packet, t.audioEncCtx.time_base, outStreams[t.audioStreamIdx].time_base)

		status = C.av_interleaved_write_frame(t.outputCtx, packet)
		C.av_packet_unref(packet)
		if status < 0 {
			return fmt.Errorf("%d: couldn't write audio packet", status)
		}
	}
}

// writeAudioPassthrough writes an audio packet directly without re-encoding
func (t *Transcoder) writeAudioPassthrough(packet *Packet) error {
	// Get the raw packet from the input
	rawPacket := C.av_packet_alloc()
	if rawPacket == nil {
		return fmt.Errorf("couldn't allocate packet for passthrough")
	}
	defer C.av_packet_free(&rawPacket)

	// Reference the original packet data
	status := C.av_packet_ref(rawPacket, t.media.packet)
	if status < 0 {
		return fmt.Errorf("%d: couldn't reference packet", status)
	}

	rawPacket.stream_index = C.int(t.audioStreamIdx)

	// Rescale timestamps from input to output
	inStream := t.audioStream.innerStream()
	outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
	C.av_packet_rescale_ts(rawPacket, inStream.time_base, outStreams[t.audioStreamIdx].time_base)

	status = C.av_interleaved_write_frame(t.outputCtx, rawPacket)
	if status < 0 {
		return fmt.Errorf("%d: couldn't write passthrough audio packet", status)
	}

	return nil
}

// writeVideoPassthrough writes a video packet directly without re-encoding
func (t *Transcoder) writeVideoPassthrough(packet *Packet) error {
	// Get the raw packet from the input
	rawPacket := C.av_packet_alloc()
	if rawPacket == nil {
		return fmt.Errorf("couldn't allocate packet for passthrough")
	}
	defer C.av_packet_free(&rawPacket)

	// Reference the original packet data
	status := C.av_packet_ref(rawPacket, t.media.packet)
	if status < 0 {
		return fmt.Errorf("%d: couldn't reference packet", status)
	}

	rawPacket.stream_index = C.int(t.videoStreamIdx)

	// Rescale timestamps from input to output
	inStream := t.videoStream.innerStream()
	outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
	C.av_packet_rescale_ts(rawPacket, inStream.time_base, outStreams[t.videoStreamIdx].time_base)

	status = C.av_interleaved_write_frame(t.outputCtx, rawPacket)
	if status < 0 {
		return fmt.Errorf("%d: couldn't write passthrough video packet", status)
	}

	return nil
}

// flushEncoder flushes remaining frames from video and audio encoders
func (t *Transcoder) flushEncoder(packet *C.AVPacket) {
	// Flush video encoder (not needed for passthrough)
	if t.videoEncCtx != nil && !t.videoPassthrough {
		C.avcodec_send_frame(t.videoEncCtx, nil)

		for {
			status := C.avcodec_receive_packet(t.videoEncCtx, packet)
			if status < 0 {
				break
			}

			packet.stream_index = C.int(t.videoStreamIdx)
			outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
			C.av_packet_rescale_ts(packet, t.videoEncCtx.time_base, outStreams[t.videoStreamIdx].time_base)
			C.av_interleaved_write_frame(t.outputCtx, packet)
			C.av_packet_unref(packet)
		}
	}

	// Flush audio encoder (not needed for passthrough)
	if t.audioEncCtx != nil && !t.audioPassthrough {
		C.avcodec_send_frame(t.audioEncCtx, nil)

		for {
			status := C.avcodec_receive_packet(t.audioEncCtx, packet)
			if status < 0 {
				break
			}

			packet.stream_index = C.int(t.audioStreamIdx)
			outStreams := unsafe.Slice(t.outputCtx.streams, t.outputCtx.nb_streams)
			C.av_packet_rescale_ts(packet, t.audioEncCtx.time_base, outStreams[t.audioStreamIdx].time_base)
			C.av_interleaved_write_frame(t.outputCtx, packet)
			C.av_packet_unref(packet)
		}
	}
}

// setupEncoders creates video and audio encoder contexts
func (t *Transcoder) setupEncoders() error {
	streamIdx := 0

	// Setup video passthrough if requested
	if t.videoCodec == "copy" && t.videoStream != nil {
		t.videoPassthrough = true

		outStream := C.avformat_new_stream(t.outputCtx, nil)
		if outStream == nil {
			return fmt.Errorf("couldn't create output video stream")
		}

		// Copy codec parameters from input stream
		status := C.avcodec_parameters_copy(outStream.codecpar, t.videoStream.CodecParameters())
		if status < 0 {
			return fmt.Errorf("%d: couldn't copy video codec parameters", status)
		}
		outStream.time_base = t.videoStream.innerStream().time_base
		t.videoStreamIdx = streamIdx
		streamIdx++
	}

	// Setup video encoder if requested
	if t.videoCodec != "" && t.videoCodec != "copy" && t.videoStream != nil {
		codec, err := findEncoder(t.videoCodec)
		if err != nil {
			return err
		}

		// Get decoder context
		decCtx := t.videoStream.DecoderContext()
		if decCtx == nil {
			return fmt.Errorf("video stream not opened for decoding")
		}

		// Determine output dimensions - may be modified by filter
		width := t.videoStream.Width()
		height := t.videoStream.Height()

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
					aspectRatio := float64(t.videoStream.Width()) / float64(t.videoStream.Height())
					height = (int(float64(width)/aspectRatio) + 1) / 2 * 2
				}
			}
		}

		// Ensure even dimensions for YUV420P codecs (libx264, libx265, etc.)
		if width%2 != 0 || height%2 != 0 {
			width = width &^ 1
			height = height &^ 1
			if t.videoFilterSpec == "" {
				t.videoFilterSpec = fmt.Sprintf("scale=%d:%d", width, height)
			}
		}

		// Get frame rate for time base
		frNum, frDen := t.videoStream.FrameRate()
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

		// Always setup filter graph for pixel format conversion (e.g., PAL8/BGRA → YUV420P).
		// Without this, raw decoded frames (e.g., from GIFs) have stride=0 for YUV planes,
		// causing "Input picture width is greater than stride" errors.
		{
			filterSpec := buildFilterSpec(t.videoFilterSpec, "yuv420p")

			tbNum, tbDen := t.videoStream.TimeBase()
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
		t.videoStreamIdx = streamIdx
		streamIdx++
	}

	// Setup audio encoder/passthrough if requested
	if t.audioCodec != "" && t.audioStream != nil {
		if t.audioCodec == "copy" {
			// Audio passthrough - just copy codec parameters
			t.audioPassthrough = true

			outStream := C.avformat_new_stream(t.outputCtx, nil)
			if outStream == nil {
				return fmt.Errorf("couldn't create output audio stream")
			}

			// Copy codec parameters from input stream
			status := C.avcodec_parameters_copy(outStream.codecpar, t.audioStream.CodecParameters())
			if status < 0 {
				return fmt.Errorf("%d: couldn't copy audio codec parameters", status)
			}
			outStream.time_base = t.audioStream.innerStream().time_base
			t.audioStreamIdx = streamIdx
			streamIdx++
		} else {
			// Re-encode audio
			codec, err := findEncoder(t.audioCodec)
			if err != nil {
				return err
			}

			// Get decoder context
			decCtx := t.audioStream.DecoderContext()
			if decCtx == nil {
				return fmt.Errorf("audio stream not opened for decoding")
			}

			// Create audio encoder with matching parameters
			t.audioEncCtx, err = createAudioEncoder(
				codec,
				t.audioStream.SampleRate(),
				decCtx.ch_layout,
				C.int(decCtx.sample_fmt),
				128000, // 128 kbps default
			)
			if err != nil {
				return fmt.Errorf("create audio encoder: %w", err)
			}

			// Setup audio filter graph if filter specified
			if t.audioFilterSpec != "" {
				tbNum, tbDen := t.audioStream.TimeBase()
				inputTimeBase := C.AVRational{num: C.int(tbNum), den: C.int(tbDen)}

				// Build filter spec with output format conversion
				filterSpec := buildAudioFilterSpec(
					t.audioFilterSpec,
					getSampleFormatName(C.int(t.audioEncCtx.sample_fmt)),
					int(t.audioEncCtx.sample_rate),
					getChannelLayoutName(&t.audioEncCtx.ch_layout),
				)

				t.audioFilterCtx, err = initAudioFilterGraph(decCtx, t.audioEncCtx, inputTimeBase, filterSpec)
				if err != nil {
					return fmt.Errorf("init audio filter: %w", err)
				}
			}

			// Add audio stream to output
			outStream := C.avformat_new_stream(t.outputCtx, nil)
			if outStream == nil {
				return fmt.Errorf("couldn't create output audio stream")
			}
			C.avcodec_parameters_from_context(outStream.codecpar, t.audioEncCtx)
			outStream.time_base = t.audioEncCtx.time_base
			t.audioStreamIdx = streamIdx
			streamIdx++
		}
	}

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

	// Handle faststart: strip from movflags and relocate moov ourselves after
	// writing, because FFmpeg's built-in faststart requires re-opening the
	// output file by URL, which isn't possible with custom AVIO contexts.
	if movflags, ok := t.formatOpts["movflags"]; ok {
		cleaned, hasFaststart := stripFaststart(movflags)
		if hasFaststart {
			t.needsFaststart = true
			if cleaned == "" {
				delete(t.formatOpts, "movflags")
			} else {
				t.formatOpts["movflags"] = cleaned
			}
		}
	}

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

	// Close streams (only close if we opened for decoding, not passthrough)
	if t.videoStream != nil && !t.videoPassthrough {
		t.videoStream.Close()
	}
	t.videoStream = nil
	if t.audioStream != nil && !t.audioPassthrough {
		t.audioStream.Close()
	}
	t.audioStream = nil

	// Close media (created from input ReadSeeker)
	if t.media != nil {
		t.media.CloseDecode()
		t.media.Close()
		t.media = nil
	}
}
