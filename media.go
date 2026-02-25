package reisen

// #cgo pkg-config: libavformat libavcodec libavutil libswscale libwebp
// #include <libavcodec/avcodec.h>
// #include <libavformat/avformat.h>
// #include <libavformat/avio.h>
// #include <libavutil/avconfig.h>
// #include <libswscale/swscale.h>
// #include <libavcodec/bsf.h>
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

// Media is a media file containing
// audio, video and other types of streams.
type Media struct {
	ctx      *C.AVFormatContext
	ioctx    *C.AVIOContext
	ioBuffer unsafe.Pointer // for custom AVIO context cleanup
	readerID uintptr        // registry key for custom reader (0 if file-based)
	packet   *C.AVPacket
	streams  []Stream
}

// StreamCount returns the number of streams.
func (media *Media) StreamCount() int {
	return int(media.ctx.nb_streams)
}

// Streams returns a slice of all the available
// media data streams.
func (media *Media) Streams() []Stream {
	streams := make([]Stream, len(media.streams))
	copy(streams, media.streams)

	return streams
}

// VideoStreams returns all the
// video streams of the media file.
func (media *Media) VideoStreams() []*VideoStream {
	videoStreams := []*VideoStream{}

	for _, stream := range media.streams {
		if videoStream, ok := stream.(*VideoStream); ok {
			videoStreams = append(videoStreams, videoStream)
		}
	}

	return videoStreams
}

// AudioStreams returns all the
// audio streams of the media file.
func (media *Media) AudioStreams() []*AudioStream {
	audioStreams := []*AudioStream{}

	for _, stream := range media.streams {
		if audioStream, ok := stream.(*AudioStream); ok {
			audioStreams = append(audioStreams, audioStream)
		}
	}

	return audioStreams
}

// Duration returns the overall duration
// of the media file.
func (media *Media) Duration() (time.Duration, error) {
	dur := media.ctx.duration
	if dur <= 0 {
		return 0, nil
	}
	tm := float64(dur) / float64(TimeBase)

	return time.ParseDuration(fmt.Sprintf("%fs", tm))
}

// FormatName returns the name of the media format.
func (media *Media) FormatName() string {
	if media.ctx.iformat.name == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.name)
}

// FormatLongName returns the long name
// of the media container.
func (media *Media) FormatLongName() string {
	if media.ctx.iformat.long_name == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.long_name)
}

// FormatMIMEType returns the MIME type name
// of the media container.
func (media *Media) FormatMIMEType() string {
	if media.ctx.iformat.mime_type == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.mime_type)
}

// findStreams retrieves the stream information
// from the media container.
func (media *Media) findStreams() error {
	streams := []Stream{}
	status := C.avformat_find_stream_info(media.ctx, nil)

	if status < 0 {
		return fmt.Errorf(
			"couldn't find stream information")
	}

	innerStreams := unsafe.Slice(
		media.ctx.streams, media.ctx.nb_streams)

	for _, innerStream := range innerStreams {
		codecParams := innerStream.codecpar
		codec := C.avcodec_find_decoder(codecParams.codec_id)

		if codec == nil {
			unknownStream := new(UnknownStream)
			unknownStream.inner = innerStream
			unknownStream.codecParams = codecParams
			unknownStream.media = media

			streams = append(streams, unknownStream)

			continue
		}

		switch codecParams.codec_type {
		case C.AVMEDIA_TYPE_VIDEO:
			videoStream := new(VideoStream)
			videoStream.inner = innerStream
			videoStream.codecParams = codecParams
			videoStream.codec = codec
			videoStream.media = media

			streams = append(streams, videoStream)

		case C.AVMEDIA_TYPE_AUDIO:
			audioStream := new(AudioStream)
			audioStream.inner = innerStream
			audioStream.codecParams = codecParams
			audioStream.codec = codec
			audioStream.media = media

			streams = append(streams, audioStream)

		default:
			unknownStream := new(UnknownStream)
			unknownStream.inner = innerStream
			unknownStream.codecParams = codecParams
			unknownStream.codec = codec
			unknownStream.media = media

			streams = append(streams, unknownStream)
		}
	}

	media.streams = streams

	return nil
}

// OpenDecode opens the media container for decoding.
//
// CloseDecode() should be called afterwards.
func (media *Media) OpenDecode() error {
	media.packet = C.av_packet_alloc()

	if media.packet == nil {
		return fmt.Errorf(
			"couldn't allocate a new packet")
	}
	trackAlloc(ResAVPacket, unsafe.Pointer(media.packet))

	return nil
}

// ReadPacket reads the next packet from the media stream.
func (media *Media) ReadPacket() (*Packet, bool, error) {
	status := C.av_read_frame(media.ctx, media.packet)

	if status < 0 {
		if status == C.int(ErrorAgain) {
			return nil, true, nil
		}

		// No packets anymore.
		return nil, false, nil
	}

	// Filter the packet if needed.
	packetStream := media.streams[media.packet.stream_index]
	outPacket := media.packet

	if packetStream.filter() != nil {
		filter := packetStream.filter()
		packetIn := packetStream.filterIn()
		packetOut := packetStream.filterOut()

		status = C.av_packet_ref(packetIn, media.packet)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't reference the packet",
					status)
		}

		status = C.av_bsf_send_packet(filter, packetIn)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't send the packet to the filter",
					status)
		}

		status = C.av_bsf_receive_packet(filter, packetOut)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't receive the packet from the filter",
					status)
		}

		outPacket = packetOut
	}

	return newPacket(media, outPacket), true, nil
}

// CloseDecode closes the media container for decoding.
func (media *Media) CloseDecode() error {
	trackFree(unsafe.Pointer(media.packet))
	C.av_packet_free(&media.packet)
	media.packet = nil

	return nil
}

// Close closes the media container.
func (media *Media) Close() {
	if media.ctx == nil {
		return
	}

	// avformat_close_input closes the input, frees the format context,
	// and sets the pointer to NULL. No separate avformat_free_context needed.
	trackFree(unsafe.Pointer(media.ctx))
	C.avformat_close_input(&media.ctx)

	// Clean up custom AVIO context if present
	// Note: avio_context_free does NOT free the buffer, we must do it.
	// avformat_close_input does NOT free custom AVIO contexts — only
	// contexts it allocated itself via avformat_open_input with a URL.
	if media.ioctx != nil {
		if media.ioctx.buffer != nil {
			C.av_free(unsafe.Pointer(media.ioctx.buffer))
		}
		C.avio_context_free(&media.ioctx)
		media.ioctx = nil
	}
	media.ioBuffer = nil

	if media.readerID != 0 {
		unregisterReader(media.readerID)
		media.readerID = 0
	}

	// ctx already freed by avformat_close_input above
	media.ctx = nil
}

// NewMedia returns a new media container analyzer
// for the specified media file.
// https://ffmpeg.org/doxygen/trunk/doc_2examples_2avio_reading_8c-example.html
func NewMedia(filename string) (*Media, error) {
	media := &Media{
		ctx: C.avformat_alloc_context(),
	}

	if media.ctx == nil {
		return nil, fmt.Errorf(
			"couldn't create a new media context")
	}
	trackAlloc(ResAVFormatContext, unsafe.Pointer(media.ctx))

	fname := C.CString(filename)
	defer C.free(unsafe.Pointer(fname))
	status := C.avformat_open_input(&media.ctx, fname, nil, nil)

	if status < 0 {
		// Note: avformat_open_input frees ctx on failure and sets it to NULL
		return nil, fmt.Errorf(
			"couldn't open file %s", filename)
	}

	err := media.findStreams()
	if err != nil {
		media.Close()
		return nil, err
	}

	return media, nil
}
