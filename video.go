package reisen

// #cgo pkg-config: libavutil libavformat libavcodec libswscale
// #include <libavcodec/avcodec.h>
// #include <libavformat/avformat.h>
// #include <libavutil/avutil.h>
// #include <libavutil/imgutils.h>
// #include <libswscale/swscale.h>
// #include <inttypes.h>
//
// // Remap deprecated YUVJ pixel formats to standard YUV equivalents.
// // Returns 1 if the format was remapped (caller should set full color range).
// static int normalizePixFmt(enum AVPixelFormat *fmt) {
//     switch (*fmt) {
//     case AV_PIX_FMT_YUVJ420P: *fmt = AV_PIX_FMT_YUV420P; return 1;
//     case AV_PIX_FMT_YUVJ422P: *fmt = AV_PIX_FMT_YUV422P; return 1;
//     case AV_PIX_FMT_YUVJ444P: *fmt = AV_PIX_FMT_YUV444P; return 1;
//     case AV_PIX_FMT_YUVJ440P: *fmt = AV_PIX_FMT_YUV440P; return 1;
//     default: return 0;
//     }
// }
import "C"

import (
	"fmt"
	"unsafe"
)

// VideoStream is a streaming holding
// video frames.
type VideoStream struct {
	baseStream
	swsCtx    *C.struct_SwsContext
	rgbaFrame *C.AVFrame
	bufSize   C.int
	// Stored for lazy SWS context init (image codecs where pix_fmt is unknown until first decode)
	targetWidth  C.int
	targetHeight C.int
	targetAlg    C.int
}

// createSwsContext creates an SWS scaling context, handling deprecated YUVJ pixel
// formats by remapping them to standard YUV equivalents with full color range.
func (video *VideoStream) createSwsContext(srcFmt C.enum_AVPixelFormat) error {
	fullRange := C.normalizePixFmt(&srcFmt)

	video.swsCtx = C.sws_getContext(video.codecCtx.width,
		video.codecCtx.height, srcFmt,
		video.targetWidth, video.targetHeight,
		C.AV_PIX_FMT_RGBA, video.targetAlg, nil, nil, nil)

	if video.swsCtx == nil {
		return fmt.Errorf("couldn't create an SWS context")
	}

	if fullRange != 0 {
		// Set source color range to full (JPEG-style 0-255)
		var inv_table *C.int
		var table *C.int
		var srcRange, dstRange, brightness, contrast, saturation C.int
		C.sws_getColorspaceDetails(video.swsCtx, &inv_table, &srcRange, &table, &dstRange, &brightness, &contrast, &saturation)
		C.sws_setColorspaceDetails(video.swsCtx, inv_table, 1, table, dstRange, brightness, contrast, saturation)
	}

	return nil
}

// AspectRatio returns the fraction of the video
// stream frame aspect ratio (1/0 if unknown).
func (video *VideoStream) AspectRatio() (int, int) {
	return int(video.codecParams.sample_aspect_ratio.num),
		int(video.codecParams.sample_aspect_ratio.den)
}

// Width returns the width of the video
// stream frame.
func (video *VideoStream) Width() int {
	return int(video.codecParams.width)
}

// Height returns the height of the video
// stream frame.
func (video *VideoStream) Height() int {
	return int(video.codecParams.height)
}

// OpenDecode opens the video stream for
// decoding with default parameters.
func (video *VideoStream) Open() error {
	return video.OpenDecode(
		int(video.codecParams.width),
		int(video.codecParams.height),
		InterpolationBicubic)
}

// OpenDecode opens the video stream for
// decoding with the specified parameters.
func (video *VideoStream) OpenDecode(width, height int, alg InterpolationAlgorithm) error {
	err := video.open()
	if err != nil {
		return err
	}

	video.rgbaFrame = C.av_frame_alloc()

	if video.rgbaFrame == nil {
		return fmt.Errorf(
			"couldn't allocate a new RGBA frame")
	}

	video.bufSize = C.av_image_get_buffer_size(
		C.AV_PIX_FMT_RGBA, C.int(width), C.int(height), 1)

	if video.bufSize < 0 {
		return fmt.Errorf(
			"%d: couldn't get the buffer size", video.bufSize)
	}

	// Allocate with extra padding — sws_scale SIMD routines (SSE2/AVX2) can
	// write a few bytes past the last scanline.
	buf := (*C.uint8_t)(unsafe.Pointer(
		C.av_malloc(bufferSize(video.bufSize) + 64)))

	if buf == nil {
		return fmt.Errorf(
			"couldn't allocate an AV buffer")
	}

	status := C.av_image_fill_arrays(&video.rgbaFrame.data[0],
		&video.rgbaFrame.linesize[0], buf, C.AV_PIX_FMT_RGBA,
		C.int(width), C.int(height), 1)

	if status < 0 {
		return fmt.Errorf(
			"%d: couldn't fill the image arrays", status)
	}
	video.targetWidth = C.int(width)
	video.targetHeight = C.int(height)
	video.targetAlg = C.int(alg)

	// For some codecs (e.g. PNG), pix_fmt is unknown until the first frame is decoded.
	// Defer SWS context creation to ReadVideoFrame in that case.
	if video.codecCtx.pix_fmt >= 0 {
		if err := video.createSwsContext(video.codecCtx.pix_fmt); err != nil {
			return err
		}
	}

	return nil
}

// Decode decodes the next video packet into the raw frame without
// pixel format conversion. Use RawFrame() to access the decoded AVFrame.
// Returns true if a frame was decoded, false for EOF or EAGAIN.
func (video *VideoStream) Decode() (bool, error) {
	ok, err := video.read()
	if err != nil {
		return false, err
	}
	if !ok || video.skip {
		return false, nil
	}
	// Skip frames with no pixel data (can happen with some image codecs)
	if video.frame.data[0] == nil {
		return false, nil
	}
	return true, nil
}

// ReadFrame reads the next frame from the stream.
func (video *VideoStream) ReadFrame() (Frame, bool, error) {
	return video.ReadVideoFrame()
}

// ReadVideoFrame reads the next video frame
// from the video stream.
func (video *VideoStream) ReadVideoFrame() (*VideoFrame, bool, error) {
	ok, err := video.read()
	if err != nil {
		return nil, false, err
	}

	if ok && video.skip {
		return nil, true, nil
	}

	// No more data.
	if !ok {
		return nil, false, nil
	}

	// Lazy SWS context init for codecs where pix_fmt wasn't known at Open time
	if video.swsCtx == nil {
		if video.codecCtx.pix_fmt < 0 {
			return nil, false, fmt.Errorf("invalid pixel format after decoding")
		}
		if err := video.createSwsContext(video.codecCtx.pix_fmt); err != nil {
			return nil, false, err
		}
	}

	// Skip frames with no pixel data (can happen with some image codecs)
	if video.frame.data[0] == nil {
		return nil, false, nil
	}

	C.sws_scale(video.swsCtx, &video.frame.data[0],
		&video.frame.linesize[0], 0,
		video.codecCtx.height,
		&video.rgbaFrame.data[0],
		&video.rgbaFrame.linesize[0])

	data := C.GoBytes(unsafe.
		Pointer(video.rgbaFrame.data[0]),
		video.bufSize)
	frame := newVideoFrame(video, int64(video.frame.pts),
		int(video.codecCtx.width), int(video.codecCtx.height), data)

	return frame, true, nil
}

// Close closes the video stream for decoding.
func (video *VideoStream) Close() error {
	err := video.close()
	if err != nil {
		return err
	}

	// Free the manually allocated buffer first, then the frame
	if video.rgbaFrame != nil {
		if video.rgbaFrame.data[0] != nil {
			C.av_free(unsafe.Pointer(video.rgbaFrame.data[0]))
		}
		C.av_frame_free(&video.rgbaFrame)
		video.rgbaFrame = nil
	}
	if video.swsCtx != nil {
		C.sws_freeContext(video.swsCtx)
		video.swsCtx = nil
	}

	return nil
}

// CodecParameters returns the codec parameters for passthrough mode.
func (video *VideoStream) CodecParameters() *C.AVCodecParameters {
	return video.codecParams
}

// DecoderContext returns the underlying decoder context for use in transcoding.
// This is for internal use by the Transcoder.
func (video *VideoStream) DecoderContext() *C.AVCodecContext {
	return video.codecCtx
}

// RawFrame returns the underlying AVFrame after decoding.
// This is for internal use by the Transcoder.
func (video *VideoStream) RawFrame() *C.AVFrame {
	return video.frame
}
