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
	"strings"
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

// probeFormat reads the first bytes of the reader to detect the media format
// and returns the corresponding ffmpeg demuxer name. Returns "" if unknown.
func probeFormat(reader io.ReadSeeker) string {
	var header [12]byte
	n, _ := reader.Read(header[:])
	reader.Seek(0, io.SeekStart)
	if n < 4 {
		return ""
	}
	// PNG: 89 50 4E 47
	if header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47 {
		return "png_pipe"
	}
	// JPEG: FF D8 FF
	if header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return "jpeg_pipe"
	}
	// GIF: GIF8
	if header[0] == 'G' && header[1] == 'I' && header[2] == 'F' && header[3] == '8' {
		return "gif"
	}
	// WebP: RIFF....WEBP
	if n >= 12 && header[0] == 'R' && header[1] == 'I' && header[2] == 'F' && header[3] == 'F' &&
		header[8] == 'W' && header[9] == 'E' && header[10] == 'B' && header[11] == 'P' {
		return "webp_pipe"
	}
	// BMP: BM
	if header[0] == 'B' && header[1] == 'M' {
		return "bmp_pipe"
	}
	// AVIF/HEIF: ISOBMFF container with ftyp box at offset 4
	if n >= 12 && header[4] == 'f' && header[5] == 't' && header[6] == 'y' && header[7] == 'p' {
		brand := string(header[8:12])
		if brand == "avif" || brand == "avis" || brand == "mif1" || brand == "heic" || brand == "heix" {
			return "mov"
		}
	}
	// TIFF: II (little-endian) or MM (big-endian) followed by magic 42
	if n >= 4 {
		if (header[0] == 'I' && header[1] == 'I' && header[2] == 0x2A && header[3] == 0x00) ||
			(header[0] == 'M' && header[1] == 'M' && header[2] == 0x00 && header[3] == 0x2A) {
			return "tiff_pipe"
		}
	}
	return ""
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

	// 2. Detect format from magic bytes to avoid noisy probing
	formatName := probeFormat(reader)

	// 3. Register reader in registry
	id := registerReader(reader, size)

	// 4. Allocate format context
	ctx := C.avformat_alloc_context()
	if ctx == nil {
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't create format context")
	}

	// 5. Allocate AVIO buffer (4KB is FFmpeg's recommended minimum)
	const bufferSize = 4096
	ioBuffer := C.av_malloc(bufferSize)
	if ioBuffer == nil {
		C.avformat_free_context(ctx)
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't allocate IO buffer")
	}

	// 6. Create custom AVIO context
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

	// 7. Attach to format context and open with format hint if available
	ctx.pb = ioctx
	var inputFmt *C.AVInputFormat
	if formatName != "" {
		cName := C.CString(formatName)
		inputFmt = C.av_find_input_format(cName)
		C.free(unsafe.Pointer(cName))
	}
	status := C.avformat_open_input(&ctx, nil, inputFmt, nil)
	if status < 0 {
		C.avio_context_free(&ioctx)
		unregisterReader(id)
		return nil, fmt.Errorf("couldn't open input: %d", status)
	}

	// 8. Build Media and find streams
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

	// 9. For pipe demuxers (images), avformat_find_stream_info consumes the
	// data without seeking back. Reset to the beginning so ReadPacket
	// starts from the actual image data.
	if strings.HasSuffix(formatName, "_pipe") || formatName == "gif" {
		C.avio_seek(ctx.pb, 0, C.SEEK_SET)
		C.avformat_flush(ctx)
	}

	return media, nil
}
