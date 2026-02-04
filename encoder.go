package reisen

/*
#cgo pkg-config: libavcodec libavutil
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// findEncoder finds an encoder by name
func findEncoder(name string) (*C.AVCodec, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	codec := C.avcodec_find_encoder_by_name(cName)
	if codec == nil {
		return nil, fmt.Errorf("encoder not found: %s", name)
	}
	return codec, nil
}

// createVideoEncoder creates and configures a video encoder context
func createVideoEncoder(
	codec *C.AVCodec,
	width, height int,
	pixFmt C.int,
	timeBase C.AVRational,
	bitRate int64,
) (*C.AVCodecContext, error) {
	ctx := C.avcodec_alloc_context3(codec)
	if ctx == nil {
		return nil, fmt.Errorf("couldn't allocate encoder context")
	}

	ctx.width = C.int(width)
	ctx.height = C.int(height)
	ctx.pix_fmt = int32(pixFmt)
	ctx.time_base = timeBase
	ctx.bit_rate = C.int64_t(bitRate)

	// Set some reasonable defaults for x264
	ctx.gop_size = 12
	ctx.max_b_frames = 2

	status := C.avcodec_open2(ctx, codec, nil)
	if status < 0 {
		C.avcodec_free_context(&ctx)
		return nil, fmt.Errorf("%d: couldn't open encoder", status)
	}

	return ctx, nil
}

// createAudioEncoder creates and configures an audio encoder context
func createAudioEncoder(
	codec *C.AVCodec,
	sampleRate int,
	channelLayout C.AVChannelLayout,
	sampleFmt C.int,
	bitRate int64,
) (*C.AVCodecContext, error) {
	ctx := C.avcodec_alloc_context3(codec)
	if ctx == nil {
		return nil, fmt.Errorf("couldn't allocate encoder context")
	}

	ctx.sample_rate = C.int(sampleRate)
	ctx.ch_layout = channelLayout
	ctx.sample_fmt = int32(sampleFmt)
	ctx.bit_rate = C.int64_t(bitRate)
	ctx.time_base = C.AVRational{num: 1, den: C.int(sampleRate)}

	status := C.avcodec_open2(ctx, codec, nil)
	if status < 0 {
		C.avcodec_free_context(&ctx)
		return nil, fmt.Errorf("%d: couldn't open audio encoder", status)
	}

	return ctx, nil
}
