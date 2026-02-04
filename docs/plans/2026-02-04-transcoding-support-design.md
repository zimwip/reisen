# Design: Transcoding Support with Dual AVIO Contexts

## Overview

Add transcoding capabilities to reisen with dual custom AVIO contexts (read + write), full filter graph support, and a builder-pattern API. This enables on-the-fly media conversion and thumbnail/preview generation without shell-based FFmpeg piping.

## Use Cases

1. **Thumbnail/preview generation** - Extract and encode specific frames or short clips
2. **On-the-fly streaming conversion** - Convert media as it streams, output to HTTP response or similar

## API

### Builder Pattern

```go
// NewTranscoder creates a transcoder from an open Media to a writer
func NewTranscoder(input *Media, output io.WriteSeeker) *Transcoder

// Video configuration
func (t *Transcoder) VideoCodec(codec string) *Transcoder      // "libx264", "libvpx", etc.
func (t *Transcoder) VideoFilter(filter string) *Transcoder    // "scale=1280:-2"
func (t *Transcoder) NoVideo() *Transcoder                     // strip video entirely

// Audio configuration
func (t *Transcoder) AudioCodec(codec string) *Transcoder      // "aac", "libopus", etc.
func (t *Transcoder) AudioPassthrough() *Transcoder            // copy without re-encoding
func (t *Transcoder) AudioFilter(filter string) *Transcoder    // "volume=0.5"
func (t *Transcoder) NoAudio() *Transcoder                     // strip audio entirely

// Output format
func (t *Transcoder) Format(fmt string) *Transcoder            // "mp4", "webm", "gif"
func (t *Transcoder) FormatOption(key, value string) *Transcoder // movflags, etc.

// Time range (optional)
func (t *Transcoder) StartAt(d time.Duration) *Transcoder      // seek to start position
func (t *Transcoder) Duration(d time.Duration) *Transcoder     // limit output duration

// Callbacks
func (t *Transcoder) OnProgress(fn func(TranscodeStats)) *Transcoder
func (t *Transcoder) OnError(fn func(error, int64) bool) *Transcoder

// Execute
func (t *Transcoder) Run(ctx context.Context) error
```

### Core Types

```go
// Transcoder configures and runs a transcode job
type Transcoder struct {
    input       *Media
    output      io.WriteSeeker
    videoCodec  string
    audioCodec  string
    filterGraph string
    audioFilter string
    format      string
    formatOpts  map[string]string
    startAt     time.Duration
    duration    time.Duration

    onProgress  func(TranscodeStats)
    onError     func(error, int64) bool
}

// TranscodeStats provides progress information
type TranscodeStats struct {
    FramesProcessed int64
    Duration        time.Duration
    TotalDuration   time.Duration
    Progress        float64         // 0.0 to 1.0
    FPS             float64
}
```

### Example Usage

```go
media, _ := reisen.NewMediaFromReader(input)
defer media.Close()

err := reisen.NewTranscoder(media, output).
    VideoCodec("libx264").
    VideoFilter("scale=1280:-2").
    AudioCodec("aac").
    Format("mp4").
    FormatOption("movflags", "frag_keyframe+empty_moov").
    Duration(3 * time.Minute).
    OnProgress(func(s reisen.TranscodeStats) {
        log.Printf("%.1f%%", s.Progress*100)
    }).
    Run(ctx)
```

## Architecture

### Pipeline

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           Transcoder.Run()                               │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Input AVIO          Demux         Decode        Filter        Encode   │
│  (existing)                                                              │
│      │                 │             │             │             │       │
│      ▼                 ▼             ▼             ▼             ▼       │
│  ┌────────┐      ┌──────────┐   ┌────────┐   ┌──────────┐   ┌────────┐  │
│  │Reader  │─────▶│AVFormat  │──▶│AVCodec │──▶│AVFilter  │──▶│AVCodec │  │
│  │Context │      │Context   │   │(decode)│   │Graph     │   │(encode)│  │
│  └────────┘      └──────────┘   └────────┘   └──────────┘   └────────┘  │
│                                                                    │     │
│                                                       Mux          ▼     │
│                                                        │      ┌────────┐ │
│                                                        └─────▶│AVFormat│ │
│                                                                │(output)│ │
│                                                                └───┬────┘ │
│                                                                    │      │
│                                                    Output AVIO     ▼      │
│                                                       (new)   ┌────────┐  │
│                                                               │Writer  │  │
│                                                               │Context │  │
│                                                               └────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

### Component Mapping

| Component | FFmpeg API | Status |
|-----------|-----------|--------|
| Input AVIO | `avio_alloc_context` (read) | Exists in reader.go |
| Output AVIO | `avio_alloc_context` (write) | New |
| Output format context | `avformat_alloc_output_context2` | New |
| Encoder contexts | `avcodec_find_encoder_by_name` | New |
| Filter graph | `avfilter_graph_alloc` | New |

## Implementation

### Output AVIO Context (writer.go)

Mirrors reader.go but for writing. Same registry pattern to bridge Go's `io.WriteSeeker` to C callbacks.

```go
type writerContext struct {
    writer io.WriteSeeker
}

var (
    writerRegistry = make(map[uintptr]*writerContext)
    writerMu       sync.RWMutex
    writerNextID   uintptr
)

//export goWritePacket
func goWritePacket(opaque unsafe.Pointer, buf *C.uint8_t, bufSize C.int) C.int

//export goWriteSeek
func goWriteSeek(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t
```

C helper:
```c
static AVIOContext* createWriteAVIOContext(size_t opaque, uint8_t *buffer, int buffer_size) {
    return avio_alloc_context(
        buffer,
        buffer_size,
        1,  // write_flag = 1 (write mode)
        (void*)opaque,
        NULL,           // no read callback
        cWritePacket,   // write callback
        cWriteSeek      // seek callback
    );
}
```

### Filter Graph (filter.go)

Filter graph structure:
```
Video: [buffer] -> [user filters] -> [format conversion] -> [buffersink]
Audio: [abuffer] -> [user filters] -> [format conversion] -> [abuffersink]
```

```go
type filterContext struct {
    graph      *C.AVFilterGraph
    bufferSrc  *C.AVFilterContext
    bufferSink *C.AVFilterContext
}

func buildVideoFilterGraph(
    decCtx *C.AVCodecContext,
    encCtx *C.AVCodecContext,
    filterSpec string,
) (*filterContext, error)

func buildAudioFilterGraph(
    decCtx *C.AVCodecContext,
    encCtx *C.AVCodecContext,
    filterSpec string,
) (*filterContext, error)
```

### Main Loop (transcoder.go)

```go
func (t *Transcoder) Run(ctx context.Context) error {
    // 1. Setup: output format context, encoders, filter graphs, write header
    // 2. Seek if StartAt configured
    // 3. Main loop:
    //    - Check cancellation
    //    - Read packet
    //    - Check duration limit
    //    - Process video/audio packet (decode -> filter -> encode -> mux)
    //    - Handle errors via callback
    //    - Report progress
    // 4. Flush encoders
    // 5. Write trailer and cleanup
}
```

### Cleanup Order

```go
func (t *Transcoder) cleanup() {
    // 1. Free encoders
    // 2. Free filter graphs
    // 3. Close output format context (writes trailer)
    // 4. Free output AVIO
    // 5. Unregister writer
}
```

### Error Handling

- Decoder errors (corrupt frame) → callback decides continue/stop
- Encoder errors → typically fatal, callback can override
- I/O errors → typically fatal
- Filter errors → typically configuration issue, fatal

## Files

### New Files

| File | Purpose |
|------|---------|
| `writer.go` | Output AVIO context, writer registry, callbacks |
| `transcoder.go` | Transcoder type, builder methods, Run() loop |
| `filter.go` | Filter graph construction for video/audio |
| `encoder.go` | Encoder setup helpers, codec configuration |

### Modified Files

| File | Change |
|------|--------|
| `errors.go` | Add ErrorIO and transcoding-related errors |

### Unchanged (reused)

- `media.go`, `reader.go` - input side
- `stream.go`, `video.go`, `audio.go` - decoding infrastructure

## Dependencies

- Adds `libavfilter` to pkg-config requirements
- No new Go dependencies

## Design Decisions

1. **Input via existing `*Media`** - Reuses file or `io.ReadSeeker` infrastructure
2. **Output via `io.WriteSeeker`** - Supports both fragmented and traditional MP4
3. **Builder pattern API** - Clean, readable configuration
4. **Full filter graph support** - Maximum flexibility via libavfilter
5. **Configurable audio handling** - Strip, passthrough, or transcode per job
6. **Progress via callback** - Simple integration, cancellation via context
7. **Error callback controls flow** - Caller decides whether to continue on errors
8. **Deferred cleanup** - Correct resource freeing regardless of success/failure
