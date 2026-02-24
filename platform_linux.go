package reisen

import "C"

func bufferSize(maxBufferSize C.int) C.ulong {
	return C.ulong(maxBufferSize)
}

func rewindPosition(dur int64) C.long {
	return C.long(dur)
}
