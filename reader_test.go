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

func TestNewMediaFromReader_FrameOutputMatchesNewMedia(t *testing.T) {
	// Compare actual decoded frame data between NewMedia and NewMediaFromReader
	filePath := "examples/player/demo.mp4"
	const framesToCompare = 30

	// Decode frames using NewMedia (file path)
	fileFrames, err := decodeVideoFrames(t, filePath, framesToCompare, false)
	if err != nil {
		t.Fatalf("decoding with NewMedia failed: %v", err)
	}

	// Decode frames using NewMediaFromReader
	readerFrames, err := decodeVideoFrames(t, filePath, framesToCompare, true)
	if err != nil {
		t.Fatalf("decoding with NewMediaFromReader failed: %v", err)
	}

	// Compare frame counts
	if len(fileFrames) != len(readerFrames) {
		t.Fatalf("frame count mismatch: file=%d, reader=%d", len(fileFrames), len(readerFrames))
	}

	if len(fileFrames) == 0 {
		t.Fatal("no frames decoded")
	}

	t.Logf("comparing %d frames", len(fileFrames))

	// Compare each frame's pixel data
	for i := 0; i < len(fileFrames); i++ {
		fileImg := fileFrames[i].Image()
		readerImg := readerFrames[i].Image()

		// Compare dimensions
		if fileImg.Bounds() != readerImg.Bounds() {
			t.Errorf("frame %d: bounds mismatch: file=%v, reader=%v",
				i, fileImg.Bounds(), readerImg.Bounds())
			continue
		}

		// Compare pixel data
		if !bytes.Equal(fileImg.Pix, readerImg.Pix) {
			// Find first differing pixel for debugging
			for j := 0; j < len(fileImg.Pix); j++ {
				if fileImg.Pix[j] != readerImg.Pix[j] {
					t.Errorf("frame %d: pixel data differs at byte %d (file=0x%02x, reader=0x%02x)",
						i, j, fileImg.Pix[j], readerImg.Pix[j])
					break
				}
			}
		}
	}
}

// decodeVideoFrames decodes video frames from the given file path
// If useReader is true, uses NewMediaFromReader; otherwise uses NewMedia
func decodeVideoFrames(t *testing.T, filePath string, maxFrames int, useReader bool) ([]*VideoFrame, error) {
	t.Helper()

	var media *Media
	var f *os.File
	var err error

	if useReader {
		f, err = os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		media, err = NewMediaFromReader(f)
		if err != nil {
			return nil, err
		}
	} else {
		media, err = NewMedia(filePath)
		if err != nil {
			return nil, err
		}
	}
	defer media.Close()

	err = media.OpenDecode()
	if err != nil {
		return nil, err
	}
	defer media.CloseDecode()

	videoStream := media.VideoStreams()[0]
	err = videoStream.Open()
	if err != nil {
		return nil, err
	}
	defer videoStream.Close()

	var frames []*VideoFrame
	for len(frames) < maxFrames {
		packet, gotPacket, err := media.ReadPacket()
		if err != nil {
			return nil, err
		}
		if !gotPacket {
			break
		}

		if packet.Type() == StreamVideo {
			frame, gotFrame, err := videoStream.ReadVideoFrame()
			if err != nil {
				return nil, err
			}
			if gotFrame && frame != nil {
				frames = append(frames, frame)
			}
		}
	}

	return frames, nil
}

func TestNewMediaFromReader_AudioFrameOutputMatchesNewMedia(t *testing.T) {
	// Compare actual decoded audio frame data between NewMedia and NewMediaFromReader
	filePath := "examples/player/demo.mp4"
	const framesToCompare = 50

	// Decode frames using NewMedia (file path)
	fileFrames, err := decodeAudioFrames(t, filePath, framesToCompare, false)
	if err != nil {
		t.Fatalf("decoding with NewMedia failed: %v", err)
	}

	// Decode frames using NewMediaFromReader
	readerFrames, err := decodeAudioFrames(t, filePath, framesToCompare, true)
	if err != nil {
		t.Fatalf("decoding with NewMediaFromReader failed: %v", err)
	}

	// Compare frame counts
	if len(fileFrames) != len(readerFrames) {
		t.Fatalf("audio frame count mismatch: file=%d, reader=%d", len(fileFrames), len(readerFrames))
	}

	if len(fileFrames) == 0 {
		t.Fatal("no audio frames decoded")
	}

	t.Logf("comparing %d audio frames", len(fileFrames))

	// Compare each frame's audio data
	for i := 0; i < len(fileFrames); i++ {
		fileData := fileFrames[i].Data()
		readerData := readerFrames[i].Data()

		if len(fileData) != len(readerData) {
			t.Errorf("audio frame %d: data length mismatch: file=%d, reader=%d",
				i, len(fileData), len(readerData))
			continue
		}

		if !bytes.Equal(fileData, readerData) {
			t.Errorf("audio frame %d: data differs", i)
		}
	}
}

// decodeAudioFrames decodes audio frames from the given file path
func decodeAudioFrames(t *testing.T, filePath string, maxFrames int, useReader bool) ([]*AudioFrame, error) {
	t.Helper()

	var media *Media
	var f *os.File
	var err error

	if useReader {
		f, err = os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		media, err = NewMediaFromReader(f)
		if err != nil {
			return nil, err
		}
	} else {
		media, err = NewMedia(filePath)
		if err != nil {
			return nil, err
		}
	}
	defer media.Close()

	err = media.OpenDecode()
	if err != nil {
		return nil, err
	}
	defer media.CloseDecode()

	audioStream := media.AudioStreams()[0]
	err = audioStream.Open()
	if err != nil {
		return nil, err
	}
	defer audioStream.Close()

	var frames []*AudioFrame
	for len(frames) < maxFrames {
		packet, gotPacket, err := media.ReadPacket()
		if err != nil {
			return nil, err
		}
		if !gotPacket {
			break
		}

		if packet.Type() == StreamAudio {
			frame, gotFrame, err := audioStream.ReadAudioFrame()
			if err != nil {
				return nil, err
			}
			if gotFrame && frame != nil {
				frames = append(frames, frame)
			}
		}
	}

	return frames, nil
}

func TestNewMediaFromReader_ReadEntireFile(t *testing.T) {
	// Read entire file to ensure no "partial file" errors
	filePath := "examples/player/demo.mp4"

	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("failed to open test file: %v", err)
	}
	defer f.Close()

	media, err := NewMediaFromReader(f)
	if err != nil {
		t.Fatalf("NewMediaFromReader failed: %v", err)
	}
	defer media.Close()

	err = media.OpenDecode()
	if err != nil {
		t.Fatalf("OpenDecode failed: %v", err)
	}
	defer media.CloseDecode()

	// Open both video and audio streams
	videoStream := media.VideoStreams()[0]
	err = videoStream.Open()
	if err != nil {
		t.Fatalf("failed to open video stream: %v", err)
	}
	defer videoStream.Close()

	audioStream := media.AudioStreams()[0]
	err = audioStream.Open()
	if err != nil {
		t.Fatalf("failed to open audio stream: %v", err)
	}
	defer audioStream.Close()

	// Read ALL packets until EOF
	videoFrameCount := 0
	audioFrameCount := 0
	packetCount := 0

	for {
		packet, gotPacket, err := media.ReadPacket()
		if err != nil {
			t.Fatalf("ReadPacket failed at packet %d: %v", packetCount, err)
		}
		if !gotPacket {
			break // EOF
		}
		packetCount++

		switch packet.Type() {
		case StreamVideo:
			frame, gotFrame, err := videoStream.ReadVideoFrame()
			if err != nil {
				t.Fatalf("ReadVideoFrame failed at video frame %d: %v", videoFrameCount, err)
			}
			if gotFrame && frame != nil {
				videoFrameCount++
			}
		case StreamAudio:
			frame, gotFrame, err := audioStream.ReadAudioFrame()
			if err != nil {
				t.Fatalf("ReadAudioFrame failed at audio frame %d: %v", audioFrameCount, err)
			}
			if gotFrame && frame != nil {
				audioFrameCount++
			}
		}
	}

	t.Logf("read %d packets, %d video frames, %d audio frames", packetCount, videoFrameCount, audioFrameCount)

	if videoFrameCount == 0 {
		t.Error("expected at least one video frame")
	}
	if audioFrameCount == 0 {
		t.Error("expected at least one audio frame")
	}
}

func TestNewMediaFromReader_EntireFileMatchesNewMedia(t *testing.T) {
	// Compare total frame counts between NewMedia and NewMediaFromReader
	filePath := "examples/player/demo.mp4"

	// Count frames using NewMedia
	fileVideoCount, fileAudioCount, err := countAllFrames(t, filePath, false)
	if err != nil {
		t.Fatalf("counting with NewMedia failed: %v", err)
	}

	// Count frames using NewMediaFromReader
	readerVideoCount, readerAudioCount, err := countAllFrames(t, filePath, true)
	if err != nil {
		t.Fatalf("counting with NewMediaFromReader failed: %v", err)
	}

	t.Logf("NewMedia: %d video, %d audio frames", fileVideoCount, fileAudioCount)
	t.Logf("NewMediaFromReader: %d video, %d audio frames", readerVideoCount, readerAudioCount)

	if fileVideoCount != readerVideoCount {
		t.Errorf("video frame count mismatch: file=%d, reader=%d", fileVideoCount, readerVideoCount)
	}
	if fileAudioCount != readerAudioCount {
		t.Errorf("audio frame count mismatch: file=%d, reader=%d", fileAudioCount, readerAudioCount)
	}
}

func countAllFrames(t *testing.T, filePath string, useReader bool) (videoCount, audioCount int, err error) {
	t.Helper()

	var media *Media
	var f *os.File

	if useReader {
		f, err = os.Open(filePath)
		if err != nil {
			return 0, 0, err
		}
		defer f.Close()

		media, err = NewMediaFromReader(f)
		if err != nil {
			return 0, 0, err
		}
	} else {
		media, err = NewMedia(filePath)
		if err != nil {
			return 0, 0, err
		}
	}
	defer media.Close()

	err = media.OpenDecode()
	if err != nil {
		return 0, 0, err
	}
	defer media.CloseDecode()

	videoStream := media.VideoStreams()[0]
	err = videoStream.Open()
	if err != nil {
		return 0, 0, err
	}
	defer videoStream.Close()

	audioStream := media.AudioStreams()[0]
	err = audioStream.Open()
	if err != nil {
		return 0, 0, err
	}
	defer audioStream.Close()

	for {
		packet, gotPacket, err := media.ReadPacket()
		if err != nil {
			return 0, 0, err
		}
		if !gotPacket {
			break
		}

		switch packet.Type() {
		case StreamVideo:
			frame, gotFrame, err := videoStream.ReadVideoFrame()
			if err != nil {
				return 0, 0, err
			}
			if gotFrame && frame != nil {
				videoCount++
			}
		case StreamAudio:
			frame, gotFrame, err := audioStream.ReadAudioFrame()
			if err != nil {
				return 0, 0, err
			}
			if gotFrame && frame != nil {
				audioCount++
			}
		}
	}

	return videoCount, audioCount, nil
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
