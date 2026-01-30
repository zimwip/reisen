# Design: io.ReadSeeker Support for Media Creation

## Overview

Add support for creating `Media` objects from Go's `io.ReadSeeker` interface instead of only filenames. This enables reading media from embedded resources, HTTP streams, custom archives, or any other source implementing the standard Go interface.

## API

```go
// NewMediaFromReader creates a Media from an io.ReadSeeker.
// The reader's size is probed automatically via Seek.
// The caller is responsible for closing the reader after Media.Close().
func NewMediaFromReader(reader io.ReadSeeker) (*Media, error)
```

## Design Decisions

1. **Size detection**: Automatically probe via `Seek(0, SeekEnd)` then `Seek(0, SeekStart)` - cleaner API than requiring size parameter
2. **Reader lifecycle**: Caller manages the reader; `Media.Close()` does not close it - follows standard Go patterns
3. **General flexibility**: No optimization for specific use cases; works with any `io.ReadSeeker`

## Implementation

### New File: reader.go

Contains:
- Reader registry (thread-safe map storing `io.ReadSeeker` references by ID)
- CGO-exported callbacks: `goReadPacket`, `goSeek`
- `NewMediaFromReader()` constructor
- Registry helper functions

### CGO Callback Bridge

C cannot store Go pointers, so we use a registry pattern:
1. Register the `io.ReadSeeker` with a unique `uintptr` ID
2. Pass the ID as FFmpeg's opaque pointer
3. Callbacks retrieve the reader from registry using the ID

```go
//export goReadPacket
func goReadPacket(opaque unsafe.Pointer, buf *C.uint8_t, bufSize C.int) C.int

//export goSeek
func goSeek(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t
```

### Modified: media.go

**Media struct** - add fields:
- `ioBuffer unsafe.Pointer` - track allocated buffer for cleanup
- `readerID uintptr` - registry key (0 if file-based)

**Close()** - clean up custom AVIO context:
- Free AVIO context via `avio_context_free()`
- Free buffer via `av_free()`
- Unregister reader from registry

### Constructor Flow

1. Probe size via seek to end, then back to start
2. Register reader in registry, get ID
3. Allocate format context
4. Allocate 4KB AVIO buffer
5. Create AVIO context with callbacks
6. Attach to format context (`ctx.pb = ioctx`)
7. Open input with NULL filename
8. Find streams
9. Return Media (or clean up on any error)

## Files Changed

| File | Change |
|------|--------|
| `reader.go` | New file: callbacks, registry, constructor |
| `media.go` | Add struct fields, modify Close() |
