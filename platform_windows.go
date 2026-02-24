package reisen

import "C"

func bufferSize(maxBufferSize C.int) C.ulonglong {
	return C.ulonglong(maxBufferSize)
}

func channelLayout(audio *AudioStream) C.longlong {
	return C.longlong(audio.codecCtx.channel_layout)
}

func rewindPosition(dur int64) C.longlong {
	return C.longlong(dur)
}
