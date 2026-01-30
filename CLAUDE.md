# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Reisen is a Go library that wraps FFmpeg's libav libraries to extract video and audio frames from media containers. It uses CGO bindings for direct C interop with FFmpeg.

## Build Requirements

Requires FFmpeg development libraries (libavformat, libavcodec, libavutil, libswresample, libswscale) and pkg-config:

- **Arch Linux**: `sudo pacman -S ffmpeg`
- **Debian/Ubuntu**: `sudo apt install libswscale-dev libavcodec-dev libavformat-dev libswresample-dev libavutil-dev`
- **macOS**: `brew install ffmpeg`

## Common Commands

```bash
# Build the library
go build

# Run the video player example (requires a demo.mp4 in that directory)
cd examples/player && go run main.go demo.mp4

# Run the analyzer example
cd examples/analyze && go run main.go demo.mp4
```

## Architecture

The library follows a pipeline architecture for media processing:

```
Media File → Media Container → Streams → Packets → Frames
```

### Core Types

- **Media** (`media.go`): Entry point via `NewMedia(filename)`. Manages the FFmpeg format context and provides access to streams. Call `OpenDecode()` before reading packets.

- **Stream** (`stream.go`): Interface for all stream types with `baseStream` providing common implementation. Key methods: `Open()`, `ReadFrame()`, `Close()`, `Rewind()`.

- **VideoStream** (`video.go`): Decodes video packets to RGBA images. Use `ReadVideoFrame()` to get `*VideoFrame` containing an `*image.RGBA`.

- **AudioStream** (`audio.go`): Decodes audio to float64 samples (stereo, 44.1kHz). Use `ReadAudioFrame()` to get `*AudioFrame` containing raw sample bytes.

- **Packet** (`packet.go`): Encoded data from media file. Check `Type()` to determine if video or audio.

### Typical Usage Flow

1. `media, _ := reisen.NewMedia("file.mp4")`
2. `media.OpenDecode()`
3. `stream := media.VideoStreams()[0]` and `stream.Open()`
4. Loop: `packet, ok, _ := media.ReadPacket()` → `frame, ok, _ := stream.ReadVideoFrame()`
5. Clean up: `stream.Close()`, `media.CloseDecode()`, `media.Close()`

### Platform-Specific Code

Files `platform_darwin.go`, `platform_linux.go`, `platform_windows.go` handle integer type conversions for buffer sizes across platforms.

### Audio Sample Format

Audio frames use `AV_SAMPLE_FMT_DBL`: 8 bytes per sample per channel, little-endian float64, stereo layout (left sample followed by right sample).
