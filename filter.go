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

// Wrapper for av_opt_set_int_list macro which CGO cannot handle directly
static int set_int_list(void *obj, const char *name, const int *val, int term, int flags) {
    return av_opt_set_bin(obj, name, (const uint8_t *)val, sizeof(*val) * 2, flags);
}
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
func initVideoFilterGraph(
	decCtx *C.AVCodecContext,
	encCtx *C.AVCodecContext,
	timeBase C.AVRational,
	filterSpec string,
) (*filterContext, error) {
	fc := &filterContext{}

	fc.graph = C.avfilter_graph_alloc()
	if fc.graph == nil {
		return nil, fmt.Errorf("couldn't allocate filter graph")
	}

	// Get buffer source and sink filters
	bufferSrc := C.avfilter_get_by_name(C.CString("buffer"))
	bufferSink := C.avfilter_get_by_name(C.CString("buffersink"))
	if bufferSrc == nil || bufferSink == nil {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("couldn't find buffer filters")
	}

	// Create buffer source args
	args := fmt.Sprintf("video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
		decCtx.width, decCtx.height, decCtx.pix_fmt,
		timeBase.num, timeBase.den,
		decCtx.sample_aspect_ratio.num, decCtx.sample_aspect_ratio.den)

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

	// Set output pixel format using our wrapper function
	pixFmts := []C.int{C.int(encCtx.pix_fmt), -1}
	cPixFmts := C.CString("pix_fmts")
	defer C.free(unsafe.Pointer(cPixFmts))
	status = C.set_int_list(unsafe.Pointer(fc.bufferSink), cPixFmts,
		&pixFmts[0], -1, C.AV_OPT_SEARCH_CHILDREN)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't set output pixel format", status)
	}

	// Parse and link filter graph
	var outputs *C.AVFilterInOut = C.avfilter_inout_alloc()
	var inputs *C.AVFilterInOut = C.avfilter_inout_alloc()
	defer C.avfilter_inout_free(&outputs)
	defer C.avfilter_inout_free(&inputs)

	outputs.name = C.av_strdup(C.CString("in"))
	outputs.filter_ctx = fc.bufferSrc
	outputs.pad_idx = 0
	outputs.next = nil

	inputs.name = C.av_strdup(C.CString("out"))
	inputs.filter_ctx = fc.bufferSink
	inputs.pad_idx = 0
	inputs.next = nil

	cFilterSpec := C.CString(filterSpec)
	defer C.free(unsafe.Pointer(cFilterSpec))

	status = C.avfilter_graph_parse_ptr(fc.graph, cFilterSpec, &inputs, &outputs, nil)
	if status < 0 {
		C.avfilter_graph_free(&fc.graph)
		return nil, fmt.Errorf("%d: couldn't parse filter graph", status)
	}

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
