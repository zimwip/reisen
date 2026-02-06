package reisen

/*
#cgo pkg-config: libavfilter libavutil libavcodec
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/pixdesc.h>
#include <libavutil/channel_layout.h>
#include <libavutil/samplefmt.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// filterContext holds the AVFilterGraph and endpoint contexts
type filterContext struct {
	graph      *C.AVFilterGraph
	bufferSrc  *C.AVFilterContext
	bufferSink *C.AVFilterContext
}

// buildFilterSpec builds the filter description string
func buildFilterSpec(userFilter string, outputPixFmt string) string {
	if userFilter == "" {
		return fmt.Sprintf("format=%s", outputPixFmt)
	}
	return fmt.Sprintf("%s,format=%s", userFilter, outputPixFmt)
}

// buildAudioFilterSpec builds the audio filter description string
func buildAudioFilterSpec(userFilter string, sampleFmt string, sampleRate int, channelLayout string) string {
	formatFilter := fmt.Sprintf("aformat=sample_fmts=%s:sample_rates=%d:channel_layouts=%s",
		sampleFmt, sampleRate, channelLayout)
	if userFilter == "" {
		return formatFilter
	}
	return fmt.Sprintf("%s,%s", userFilter, formatFilter)
}

// initVideoFilterGraph creates a video filter graph
// Note: encCtx is kept for future use (e.g., when pix_fmts setting is fixed)
func initVideoFilterGraph(
	decCtx *C.AVCodecContext,
	_ *C.AVCodecContext, // encCtx - reserved for future use
	timeBase C.AVRational,
	filterSpec string,
) (*filterContext, error) {
	fc := &filterContext{}

	fc.graph = C.avfilter_graph_alloc()
	if fc.graph == nil {
		return nil, fmt.Errorf("couldn't allocate filter graph")
	}

	// Get buffer source and sink filters
	cBuffer := C.CString("buffer")
	cBufferSink := C.CString("buffersink")
	bufferSrc := C.avfilter_get_by_name(cBuffer)
	bufferSink := C.avfilter_get_by_name(cBufferSink)
	C.free(unsafe.Pointer(cBuffer))
	C.free(unsafe.Pointer(cBufferSink))
	if bufferSrc == nil || bufferSink == nil {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("couldn't find buffer filters")
	}

	// Create buffer source args
	// Handle invalid aspect ratio (0/0 or 0/x) by defaulting to 1/1
	aspectNum := decCtx.sample_aspect_ratio.num
	aspectDen := decCtx.sample_aspect_ratio.den
	if aspectDen == 0 || aspectNum == 0 {
		aspectNum = 1
		aspectDen = 1
	}
	args := fmt.Sprintf("video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
		decCtx.width, decCtx.height, decCtx.pix_fmt,
		timeBase.num, timeBase.den,
		aspectNum, aspectDen)

	// Create buffer source context
	cArgs := C.CString(args)
	defer C.free(unsafe.Pointer(cArgs))
	cBufSrc := C.CString("in")
	defer C.free(unsafe.Pointer(cBufSrc))

	status := C.avfilter_graph_create_filter(&fc.bufferSrc, bufferSrc, cBufSrc,
		cArgs, nil, fc.graph)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't create buffer source", status)
	}

	// Create buffer sink context
	cBufSink := C.CString("out")
	defer C.free(unsafe.Pointer(cBufSink))

	status = C.avfilter_graph_create_filter(&fc.bufferSink, bufferSink, cBufSink,
		nil, nil, fc.graph)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't create buffer sink", status)
	}

	// NOTE: We don't set pix_fmts on the buffer sink because the filter spec
	// already includes format conversion (e.g., "scale=320:-2,format=yuv420p")
	// Setting it here causes a segfault in avfilter_graph_config due to
	// memory corruption from the set_int_list wrapper function.

	// Parse and link filter graph
	// Note: avfilter_graph_parse_ptr takes ownership of the inout structures,
	// so we only free them if parsing fails
	var outputs *C.AVFilterInOut = C.avfilter_inout_alloc()
	var inputs *C.AVFilterInOut = C.avfilter_inout_alloc()

	// Create names using C strings properly
	cNameIn := C.CString("in")
	cNameOut := C.CString("out")
	outputs.name = C.av_strdup(cNameIn)
	C.free(unsafe.Pointer(cNameIn))
	outputs.filter_ctx = fc.bufferSrc
	outputs.pad_idx = 0
	outputs.next = nil

	inputs.name = C.av_strdup(cNameOut)
	C.free(unsafe.Pointer(cNameOut))
	inputs.filter_ctx = fc.bufferSink
	inputs.pad_idx = 0
	inputs.next = nil

	cFilterSpec := C.CString(filterSpec)
	status = C.avfilter_graph_parse_ptr(fc.graph, cFilterSpec, &inputs, &outputs, nil)
	C.free(unsafe.Pointer(cFilterSpec))

	if status < 0 {
		// Only free on error - on success, parse_ptr takes ownership
		C.avfilter_inout_free(&inputs)
		C.avfilter_inout_free(&outputs)
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't parse filter graph", status)
	}

	// Free any unlinked inout after successful parse
	C.avfilter_inout_free(&inputs)
	C.avfilter_inout_free(&outputs)

	status = C.avfilter_graph_config(fc.graph, nil)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't configure filter graph", status)
	}

	return fc, nil
}

// close frees the filter graph resources
func (fc *filterContext) close() {
	if fc.graph != nil {
		C.avfilter_graph_free(&fc.graph)
		fc.graph = nil
	}
}

// Sample format constants (from libavutil/samplefmt.h)
const (
	sampleFmtU8   = 0  // unsigned 8 bits
	sampleFmtS16  = 1  // signed 16 bits
	sampleFmtS32  = 2  // signed 32 bits
	sampleFmtFlt  = 3  // float
	sampleFmtDbl  = 4  // double
	sampleFmtU8P  = 5  // unsigned 8 bits, planar
	sampleFmtS16P = 6  // signed 16 bits, planar
	sampleFmtS32P = 7  // signed 32 bits, planar
	sampleFmtFltP = 8  // float, planar
	sampleFmtDblP = 9  // double, planar
	sampleFmtS64  = 10 // signed 64 bits
	sampleFmtS64P = 11 // signed 64 bits, planar
)

// getSampleFormatName returns the FFmpeg sample format name
func getSampleFormatName(sampleFmt C.int) string {
	switch int(sampleFmt) {
	case sampleFmtU8:
		return "u8"
	case sampleFmtS16:
		return "s16"
	case sampleFmtS32:
		return "s32"
	case sampleFmtFlt:
		return "flt"
	case sampleFmtDbl:
		return "dbl"
	case sampleFmtU8P:
		return "u8p"
	case sampleFmtS16P:
		return "s16p"
	case sampleFmtS32P:
		return "s32p"
	case sampleFmtFltP:
		return "fltp"
	case sampleFmtDblP:
		return "dblp"
	case sampleFmtS64:
		return "s64"
	case sampleFmtS64P:
		return "s64p"
	default:
		return "flt" // default fallback
	}
}

// getChannelLayoutName returns the channel layout name based on channel count
func getChannelLayoutName(chLayout *C.AVChannelLayout) string {
	nbChannels := chLayout.nb_channels
	switch nbChannels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%dC", nbChannels)
	}
}

// initAudioFilterGraph creates an audio filter graph
func initAudioFilterGraph(
	decCtx *C.AVCodecContext,
	encCtx *C.AVCodecContext,
	timeBase C.AVRational,
	filterSpec string,
) (*filterContext, error) {
	fc := &filterContext{}

	fc.graph = C.avfilter_graph_alloc()
	if fc.graph == nil {
		return nil, fmt.Errorf("couldn't allocate audio filter graph")
	}

	// Get abuffer source and abuffersink filters
	cAbuffer := C.CString("abuffer")
	cAbufferSink := C.CString("abuffersink")
	abufferSrc := C.avfilter_get_by_name(cAbuffer)
	abufferSink := C.avfilter_get_by_name(cAbufferSink)
	C.free(unsafe.Pointer(cAbuffer))
	C.free(unsafe.Pointer(cAbufferSink))
	if abufferSrc == nil || abufferSink == nil {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("couldn't find audio buffer filters")
	}

	// Get channel layout and sample format names
	chLayoutStr := getChannelLayoutName(&decCtx.ch_layout)
	sampleFmtStr := getSampleFormatName(C.int(decCtx.sample_fmt))

	// Create buffer source args
	args := fmt.Sprintf("time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=%s",
		timeBase.num, timeBase.den,
		decCtx.sample_rate,
		sampleFmtStr,
		chLayoutStr)

	// Create buffer source context
	cArgs := C.CString(args)
	defer C.free(unsafe.Pointer(cArgs))
	cBufSrc := C.CString("in")
	defer C.free(unsafe.Pointer(cBufSrc))

	status := C.avfilter_graph_create_filter(&fc.bufferSrc, abufferSrc, cBufSrc,
		cArgs, nil, fc.graph)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't create audio buffer source", status)
	}

	// Create buffer sink context
	cBufSink := C.CString("out")
	defer C.free(unsafe.Pointer(cBufSink))

	status = C.avfilter_graph_create_filter(&fc.bufferSink, abufferSink, cBufSink,
		nil, nil, fc.graph)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't create audio buffer sink", status)
	}

	// Parse and link filter graph
	var outputs *C.AVFilterInOut = C.avfilter_inout_alloc()
	var inputs *C.AVFilterInOut = C.avfilter_inout_alloc()

	cNameIn := C.CString("in")
	cNameOut := C.CString("out")
	outputs.name = C.av_strdup(cNameIn)
	C.free(unsafe.Pointer(cNameIn))
	outputs.filter_ctx = fc.bufferSrc
	outputs.pad_idx = 0
	outputs.next = nil

	inputs.name = C.av_strdup(cNameOut)
	C.free(unsafe.Pointer(cNameOut))
	inputs.filter_ctx = fc.bufferSink
	inputs.pad_idx = 0
	inputs.next = nil

	cFilterSpec := C.CString(filterSpec)
	status = C.avfilter_graph_parse_ptr(fc.graph, cFilterSpec, &inputs, &outputs, nil)
	C.free(unsafe.Pointer(cFilterSpec))

	if status < 0 {
		C.avfilter_inout_free(&inputs)
		C.avfilter_inout_free(&outputs)
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't parse audio filter graph", status)
	}

	// Free any unlinked inout after successful parse
	C.avfilter_inout_free(&inputs)
	C.avfilter_inout_free(&outputs)

	status = C.avfilter_graph_config(fc.graph, nil)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't configure audio filter graph", status)
	}

	return fc, nil
}
