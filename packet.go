package reisen

// #cgo pkg-config: libavformat libavcodec libavutil libswscale
// #include <libavcodec/avcodec.h>
// #include <libavformat/avformat.h>
// #include <libavutil/avconfig.h>
// #include <libswscale/swscale.h>
import "C"
import "unsafe"

// Packet is a piece of encoded data
// acquired from the media container.
//
// It can be either a video frame or
// an audio frame.
type Packet struct {
	media       *Media
	streamIndex int
	data        []byte
	pts         int64
	dts         int64
	pos         int64
	duration    int64
	size        int
	flags       int
}

// StreamIndex returns the index of the
// stream the packet belongs to.
func (pkt *Packet) StreamIndex() int {
	return pkt.streamIndex
}

// Type returns the type of the packet
// (video or audio).
func (pkt *Packet) Type() StreamType {
	return pkt.media.Streams()[pkt.streamIndex].Type()
}

// Data returns a copy of the data encoded in the packet.
// The underlying C packet data is only valid until the next ReadPacket call,
// so this copies on demand. Returns nil if packet data is no longer available.
func (pkt *Packet) Data() []byte {
	if pkt.data == nil {
		return nil
	}
	buf := make([]byte, pkt.size)
	copy(buf, pkt.data)
	return buf
}

// RawData returns the packet data slice directly without copying.
// WARNING: The returned slice is only valid until the next ReadPacket call.
func (pkt *Packet) RawData() []byte {
	return pkt.data
}

// Returns the size of the
// packet data.
func (pkt *Packet) Size() int {
	return pkt.size
}

// newPacket creates a
// new packet info object.
func newPacket(media *Media, cPkt *C.AVPacket) *Packet {
	pkt := &Packet{
		media:       media,
		streamIndex: int(cPkt.stream_index),
		pts:         int64(cPkt.pts),
		dts:         int64(cPkt.dts),
		pos:         int64(cPkt.pos),
		duration:    int64(cPkt.duration),
		size:        int(cPkt.size),
		flags:       int(cPkt.flags),
	}

	// Provide zero-copy access to packet data via unsafe slice.
	// Valid only until the next ReadPacket call (C packet is reused).
	if cPkt.data != nil && cPkt.size > 0 {
		pkt.data = unsafe.Slice((*byte)(unsafe.Pointer(cPkt.data)), int(cPkt.size))
	}

	return pkt
}
