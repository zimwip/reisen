package reisen

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestNewMediaFromReader_WithFileReader(t *testing.T) {
	// Open demo.mp4 as a regular file, then wrap it as ReadSeeker
	f, err := os.Open("examples/player/demo.mp4")
	if err != nil {
		t.Fatalf("failed to open test file: %v", err)
	}
	defer f.Close()

	// Create media from ReadSeeker
	media, err := NewMediaFromReader(f)
	if err != nil {
		t.Fatalf("NewMediaFromReader failed: %v", err)
	}
	defer media.Close()

	// Verify we can access streams like with NewMedia
	if media.StreamCount() == 0 {
		t.Error("expected at least one stream")
	}

	videoStreams := media.VideoStreams()
	if len(videoStreams) == 0 {
		t.Error("expected at least one video stream")
	}

	audioStreams := media.AudioStreams()
	if len(audioStreams) == 0 {
		t.Error("expected at least one audio stream")
	}
}

func TestNewMediaFromReader_WithBytesReader(t *testing.T) {
	// Read entire file into memory
	data, err := os.ReadFile("examples/player/demo.mp4")
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}

	// Create media from bytes.Reader (pure in-memory)
	reader := bytes.NewReader(data)
	media, err := NewMediaFromReader(reader)
	if err != nil {
		t.Fatalf("NewMediaFromReader failed: %v", err)
	}
	defer media.Close()

	// Verify streams
	if media.StreamCount() == 0 {
		t.Error("expected at least one stream")
	}
}

func TestNewMediaFromReader_CanDecodeFrames(t *testing.T) {
	f, err := os.Open("examples/player/demo.mp4")
	if err != nil {
		t.Fatalf("failed to open test file: %v", err)
	}
	defer f.Close()

	media, err := NewMediaFromReader(f)
	if err != nil {
		t.Fatalf("NewMediaFromReader failed: %v", err)
	}
	defer media.Close()

	// Open for decoding
	err = media.OpenDecode()
	if err != nil {
		t.Fatalf("OpenDecode failed: %v", err)
	}
	defer media.CloseDecode()

	videoStream := media.VideoStreams()[0]
	err = videoStream.Open()
	if err != nil {
		t.Fatalf("failed to open video stream: %v", err)
	}
	defer videoStream.Close()

	// Read at least one video frame
	frameCount := 0
	for frameCount < 10 {
		packet, gotPacket, err := media.ReadPacket()
		if err != nil {
			t.Fatalf("ReadPacket failed: %v", err)
		}
		if !gotPacket {
			break
		}

		if packet.Type() == StreamVideo {
			frame, gotFrame, err := videoStream.ReadVideoFrame()
			if err != nil {
				t.Fatalf("ReadVideoFrame failed: %v", err)
			}
			if gotFrame && frame != nil {
				frameCount++
			}
		}
	}

	if frameCount == 0 {
		t.Error("expected to decode at least one video frame")
	}
}

func TestNewMediaFromReader_MatchesNewMedia(t *testing.T) {
	// Compare results between NewMedia and NewMediaFromReader
	filePath := "examples/player/demo.mp4"

	// Using NewMedia (file path)
	mediaFile, err := NewMedia(filePath)
	if err != nil {
		t.Fatalf("NewMedia failed: %v", err)
	}
	defer mediaFile.Close()

	// Using NewMediaFromReader
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open test file: %v", err)
	}
	defer f.Close()

	mediaReader, err := NewMediaFromReader(f)
	if err != nil {
		t.Fatalf("NewMediaFromReader failed: %v", err)
	}
	defer mediaReader.Close()

	// Compare metadata
	if mediaFile.StreamCount() != mediaReader.StreamCount() {
		t.Errorf("stream count mismatch: file=%d, reader=%d",
			mediaFile.StreamCount(), mediaReader.StreamCount())
	}

	if mediaFile.FormatName() != mediaReader.FormatName() {
		t.Errorf("format name mismatch: file=%q, reader=%q",
			mediaFile.FormatName(), mediaReader.FormatName())
	}

	fileDuration, _ := mediaFile.Duration()
	readerDuration, _ := mediaReader.Duration()
	if fileDuration != readerDuration {
		t.Errorf("duration mismatch: file=%v, reader=%v",
			fileDuration, readerDuration)
	}
}

func TestNewMediaFromReader_SeekError(t *testing.T) {
	// Test with a reader that fails to seek
	reader := &failingSeeker{data: []byte("not a video")}
	_, err := NewMediaFromReader(reader)
	if err == nil {
		t.Error("expected error for failing seeker")
	}
}

// failingSeeker is an io.ReadSeeker that fails on Seek
type failingSeeker struct {
	data []byte
	pos  int
}

func (f *failingSeeker) Read(p []byte) (n int, err error) {
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}
	n = copy(p, f.data[f.pos:])
	f.pos += n
	return n, nil
}

func (f *failingSeeker) Seek(offset int64, whence int) (int64, error) {
	return 0, io.ErrNoProgress
}
