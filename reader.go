package reisen

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

// Forward declarations - these are implemented as Go exports below
int goReadPacket(void *opaque, uint8_t *buf, int buf_size);
int64_t goSeek(void *opaque, int64_t offset, int whence);

// C wrapper functions that FFmpeg will call
static int cReadPacket(void *opaque, uint8_t *buf, int buf_size) {
    return goReadPacket(opaque, buf, buf_size);
}

static int64_t cSeek(void *opaque, int64_t offset, int whence) {
    return goSeek(opaque, offset, whence);
}

// Helper to create AVIO context with our callbacks
// Takes size_t as opaque to avoid Go's unsafe.Pointer conversion warnings
static AVIOContext* createAVIOContext(size_t opaque, uint8_t *buffer, int buffer_size) {
    return avio_alloc_context(
        buffer,
        buffer_size,
        0,  // write_flag = 0 (read-only)
        (void*)opaque,
        cReadPacket,
        NULL,  // write callback
        cSeek
    );
}
*/
import "C"

import (
	"fmt"
	"io"
	"sync"
	"unsafe"
)

// readerContext holds the Go reader and its size for use in callbacks
type readerContext struct {
	reader io.ReadSeeker
	size   int64
}

// Registry to map opaque pointers to Go readers
// (C callbacks can't hold Go pointers directly)
var (
	readerRegistry = make(map[uintptr]*readerContext)
	readerMu       sync.RWMutex
	readerNextID   uintptr
)

func registerReader(r io.ReadSeeker, size int64) uintptr {
	readerMu.Lock()
	defer readerMu.Unlock()
	readerNextID++
	id := readerNextID
	readerRegistry[id] = &readerContext{reader: r, size: size}
	return id
}

func unregisterReader(id uintptr) {
	readerMu.Lock()
	defer readerMu.Unlock()
	delete(readerRegistry, id)
}

func getReaderContext(id uintptr) *readerContext {
	readerMu.RLock()
	defer readerMu.RUnlock()
	return readerRegistry[id]
}

//export goReadPacket
func goReadPacket(opaque unsafe.Pointer, buf *C.uint8_t, bufSize C.int) C.int {
	id := uintptr(opaque)
	ctx := getReaderContext(id)
	if ctx == nil {
		return C.int(ErrorInvalidValue)
	}

	// Create Go buffer and read into it
	gobuf := make([]byte, int(bufSize))
	n, err := ctx.reader.Read(gobuf)

	if n > 0 {
		// Copy data to C buffer
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	if err == io.EOF {
		if n == 0 {
			return C.int(ErrorEndOfFile)
		}
		// Return bytes read; next call will return EOF
		return C.int(n)
	}

	if err != nil {
		return C.int(ErrorInvalidValue)
	}

	return C.int(n)
}

// AVSEEK_SIZE is FFmpeg's way of asking for the file size
const avseekSize = 0x10000

//export goSeek
func goSeek(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t {
	id := uintptr(opaque)
	ctx := getReaderContext(id)
	if ctx == nil {
		return -1
	}

	// Handle AVSEEK_SIZE - FFmpeg asking for total size
	if int(whence) == avseekSize {
		return C.int64_t(ctx.size)
	}

	// Handle AVSEEK_SIZE | SEEK_SET combination (seen in some FFmpeg versions)
	if int(whence)&avseekSize != 0 {
		return C.int64_t(ctx.size)
	}

	pos, err := ctx.reader.Seek(int64(offset), int(whence))
	if err != nil {
		return -1
	}
	return C.int64_t(pos)
}

// NewMediaFromReader creates a Media from an io.ReadSeeker.
// The reader's size is probed automatically via Seek.
// The caller is responsible for closing the reader after Media.Close().
func NewMediaFromReader(reader io.ReadSeeker) (*Media, error) {
	// 1. Probe size via seek
	size, err := reader.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("couldn't seek to end: %w", err)
	}
	_, err = reader.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("couldn't seek to start: %w", err)
	}

	// 2. Register reader in registry
	id := registerReader(reader, size)

	// 3. Allocate format context
	ctx := C.avformat_alloc_context()
	if ctx == nil {
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't create format context")
	}

	// 4. Allocate AVIO buffer (4KB is FFmpeg's recommended minimum)
	const bufferSize = 4096
	ioBuffer := C.av_malloc(bufferSize)
	if ioBuffer == nil {
		C.avformat_free_context(ctx)
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't allocate IO buffer")
	}

	// 5. Create custom AVIO context
	ioctx := C.createAVIOContext(
		C.size_t(id),
		(*C.uint8_t)(ioBuffer),
		bufferSize,
	)
	if ioctx == nil {
		C.av_free(ioBuffer)
		C.avformat_free_context(ctx)
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't create AVIO context")
	}

	// 6. Attach to format context and open
	ctx.pb = ioctx
	status := C.avformat_open_input(&ctx, nil, nil, nil)
	if status < 0 {
		C.avio_context_free(&ioctx)
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't open input: %d", status)
	}

	// 7. Build Media and find streams
	media := &Media{
		ctx:      ctx,
		ioctx:    ioctx,
		ioBuffer: ioBuffer,
		readerID: id,
	}
	if err := media.findStreams(); err != nil {
		media.Close()
		return nil, err
	}

	return media, nil
}
