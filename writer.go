package reisen

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

// Forward declarations
int goWritePacket(void *opaque, uint8_t *buf, int buf_size);
int goOutputRead(void *opaque, uint8_t *buf, int buf_size);
int64_t goWriteSeek(void *opaque, int64_t offset, int whence);

// C wrapper functions
// FFmpeg 6.1+ (libavformat >= 61) uses const uint8_t*, older versions use uint8_t*
#if LIBAVFORMAT_VERSION_MAJOR >= 61
static int cWritePacket(void *opaque, const uint8_t *buf, int buf_size) {
    return goWritePacket(opaque, (uint8_t*)buf, buf_size);
}
#else
static int cWritePacket(void *opaque, uint8_t *buf, int buf_size) {
    return goWritePacket(opaque, buf, buf_size);
}
#endif

static int cOutputRead(void *opaque, uint8_t *buf, int buf_size) {
    return goOutputRead(opaque, buf, buf_size);
}

static int64_t cWriteSeek(void *opaque, int64_t offset, int whence) {
    return goWriteSeek(opaque, offset, whence);
}

// Helper to create write-only AVIO context
static AVIOContext* createWriteAVIOContext(size_t opaque, uint8_t *buffer, int buffer_size) {
    return avio_alloc_context(
        buffer,
        buffer_size,
        1,  // write_flag = 1 (write mode)
        (void*)opaque,
        NULL,         // no read callback
        cWritePacket,
        cWriteSeek
    );
}

// Helper to create read-write AVIO context (needed for faststart)
static AVIOContext* createReadWriteAVIOContext(size_t opaque, uint8_t *buffer, int buffer_size) {
    return avio_alloc_context(
        buffer,
        buffer_size,
        1,  // write_flag = 1 (write mode)
        (void*)opaque,
        cOutputRead,  // read callback for faststart
        cWritePacket,
        cWriteSeek
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

// writerContext holds the Go writer for use in callbacks
type writerContext struct {
	writer io.WriteSeeker
	reader io.Reader // non-nil when writer also implements io.Reader (needed for faststart)
}

// Registry to map opaque pointers to Go writers
var (
	writerRegistry = make(map[uintptr]*writerContext)
	writerMu       sync.RWMutex
	writerNextID   uintptr
)

func registerWriter(w io.WriteSeeker) uintptr {
	writerMu.Lock()
	defer writerMu.Unlock()
	writerNextID++
	id := writerNextID
	wc := &writerContext{writer: w}
	if r, ok := w.(io.Reader); ok {
		wc.reader = r
	}
	writerRegistry[id] = wc
	return id
}

func unregisterWriter(id uintptr) {
	writerMu.Lock()
	defer writerMu.Unlock()
	delete(writerRegistry, id)
}

func getWriterContext(id uintptr) *writerContext {
	writerMu.RLock()
	defer writerMu.RUnlock()
	return writerRegistry[id]
}

//export goOutputRead
func goOutputRead(opaque unsafe.Pointer, buf *C.uint8_t, bufSize C.int) C.int {
	id := uintptr(opaque)
	ctx := getWriterContext(id)
	if ctx == nil || ctx.reader == nil {
		return C.int(ErrorIO)
	}

	gobuf := make([]byte, int(bufSize))
	n, err := ctx.reader.Read(gobuf)

	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	if err == io.EOF {
		if n == 0 {
			return C.int(ErrorEndOfFile)
		}
		return C.int(n)
	}

	if err != nil {
		return C.int(ErrorIO)
	}
	return C.int(n)
}

//export goWritePacket
func goWritePacket(opaque unsafe.Pointer, buf *C.uint8_t, bufSize C.int) C.int {
	id := uintptr(opaque)
	ctx := getWriterContext(id)
	if ctx == nil {
		return C.int(ErrorIO)
	}

	// Copy C buffer to Go slice and write
	data := C.GoBytes(unsafe.Pointer(buf), bufSize)
	n, err := ctx.writer.Write(data)
	if err != nil {
		return C.int(ErrorIO)
	}
	return C.int(n)
}

//export goWriteSeek
func goWriteSeek(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t {
	id := uintptr(opaque)
	ctx := getWriterContext(id)
	if ctx == nil {
		return -1
	}

	// Handle AVSEEK_SIZE - return the current file size
	if int(whence)&avseekSize != 0 {
		// Save current position
		cur, err := ctx.writer.Seek(0, io.SeekCurrent)
		if err != nil {
			return -1
		}
		// Seek to end to get size
		size, err := ctx.writer.Seek(0, io.SeekEnd)
		if err != nil {
			return -1
		}
		// Restore position
		_, err = ctx.writer.Seek(cur, io.SeekStart)
		if err != nil {
			return -1
		}
		return C.int64_t(size)
	}

	pos, err := ctx.writer.Seek(int64(offset), int(whence))
	if err != nil {
		return -1
	}
	return C.int64_t(pos)
}

// createOutputAVIO creates an AVIO context for writing.
// If the writer also implements io.Reader (e.g. *os.File), a read callback
// is provided so that FFmpeg can read back written data (required for +faststart).
func createOutputAVIO(w io.WriteSeeker) (*C.AVIOContext, uintptr, unsafe.Pointer, error) {
	id := registerWriter(w)

	const bufferSize = 4096
	ioBuffer := C.av_malloc(bufferSize)
	if ioBuffer == nil {
		unregisterWriter(id)
		return nil, 0, nil, fmt.Errorf("couldn't allocate IO buffer")
	}

	var ioctx *C.AVIOContext
	if _, ok := w.(io.Reader); ok {
		// Writer supports reading — use read-write AVIO (enables faststart)
		ioctx = C.createReadWriteAVIOContext(
			C.size_t(id),
			(*C.uint8_t)(ioBuffer),
			bufferSize,
		)
	} else {
		// Write-only
		ioctx = C.createWriteAVIOContext(
			C.size_t(id),
			(*C.uint8_t)(ioBuffer),
			bufferSize,
		)
	}
	if ioctx == nil {
		C.av_free(ioBuffer)
		unregisterWriter(id)
		return nil, 0, nil, fmt.Errorf("couldn't create write AVIO context")
	}

	return ioctx, id, ioBuffer, nil
}
