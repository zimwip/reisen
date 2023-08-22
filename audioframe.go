package reisen

// #cgo pkg-config: libavutil libavformat libavcodec  libswscale
// #include <libavcodec/avcodec.h>
// #include <libavformat/avformat.h>
// #include <libavutil/avutil.h>
// #include <libavutil/imgutils.h>
// #include <libswscale/swscale.h>
// #include <inttypes.h>
import "C"

// AudioFrame is a data frame
// obtained from an audio stream.
type AudioFrame struct {
	baseFrame
	data []byte
}

// Data returns a raw slice of
// audio frame samples.
func (frame *AudioFrame) Data() []byte {
	return frame.data
}

// newAudioFrame returns a newly created audio frame.
func newAudioFrame(stream Stream, pts int64, indCoded, indDisplay int, data []byte) *AudioFrame {
	frame := new(AudioFrame)

	frame.stream = stream
	frame.pts = pts
	frame.data = data
	frame.indexCoded = indCoded
	frame.indexDisplay = indDisplay

	return frame
}
