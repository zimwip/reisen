package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"sync"
	"time"

	"github.com/faiface/beep"
	"github.com/faiface/beep/speaker"
	"github.com/hajimehoshi/ebiten"
	_ "github.com/silbinarywolf/preferdiscretegpu"
	"github.com/zimwip/reisen"
)

const (
	frameBufferSize                   = 1024
	sampleRate                        = 44100
	channelCount                      = 2
	bitDepth                          = 8
	sampleBufferSize                  = 32 * channelCount * bitDepth * 1024
	SpeakerSampleRate beep.SampleRate = 44100
	maxReadSize                       = 4096
)

// Command-line flags
var convertFlag = flag.Bool("convert", false, "Enable transcoder-based conversion (80% scale via FFmpeg filters)")
var scalePercent = flag.Float64("scale", 0.8, "Scale factor when -convert is enabled (default 0.8 = 80%)")

// pipeWriteSeeker wraps an io.Writer to implement io.WriteSeeker
// For streaming formats like MPEG-TS, seeking is not actually needed
type pipeWriteSeeker struct {
	w   io.Writer
	pos int64
}

func (p *pipeWriteSeeker) Write(data []byte) (int, error) {
	n, err := p.w.Write(data)
	p.pos += int64(n)
	return n, err
}

func (p *pipeWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	// For MPEG-TS streaming, we don't actually need to seek
	// Just track position for reporting
	switch whence {
	case io.SeekStart:
		p.pos = offset
	case io.SeekCurrent:
		p.pos += offset
	case io.SeekEnd:
		// Can't seek to end in a pipe
		return p.pos, nil
	}
	return p.pos, nil
}

// limitedReader wraps an io.ReadSeeker and limits each read to maxReadSize bytes.
type limitedReader struct {
	r io.ReadSeeker
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if len(p) > maxReadSize {
		p = p[:maxReadSize]
	}
	return l.r.Read(p)
}

func (l *limitedReader) Seek(offset int64, whence int) (int64, error) {
	return l.r.Seek(offset, whence)
}

// ringBuffer is a thread-safe buffer that allows concurrent read/write
type ringBuffer struct {
	buf    *bytes.Buffer
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
}

func newRingBuffer() *ringBuffer {
	rb := &ringBuffer{
		buf: bytes.NewBuffer(nil),
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

func (rb *ringBuffer) Write(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := rb.buf.Write(p)
	rb.cond.Broadcast() // Signal readers
	return n, err
}

func (rb *ringBuffer) Read(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for rb.buf.Len() == 0 && !rb.closed {
		rb.cond.Wait()
	}
	if rb.buf.Len() == 0 && rb.closed {
		return 0, io.EOF
	}
	return rb.buf.Read(p)
}

func (rb *ringBuffer) Close() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.closed = true
	rb.cond.Broadcast()
	return nil
}

func (rb *ringBuffer) Seek(offset int64, whence int) (int64, error) {
	// Ring buffer doesn't support seeking - just return current position
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return int64(rb.buf.Len()), nil
}

// transcodeWithPipe transcodes the input file with the given scale factor
// using a pipe for streaming. Returns a reader for the transcoded content
// and starts transcoding in a background goroutine.
func transcodeWithPipe(inputPath string, scale float64) (io.ReadSeeker, int, int, error) {
	// Open input file to get dimensions
	inputFile, err := os.Open(inputPath)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open input: %w", err)
	}

	// Get the original dimensions
	media, err := reisen.NewMediaFromReader(&limitedReader{r: inputFile})
	if err != nil {
		inputFile.Close()
		return nil, 0, 0, fmt.Errorf("open media: %w", err)
	}

	videoStreams := media.VideoStreams()
	if len(videoStreams) == 0 {
		media.Close()
		inputFile.Close()
		return nil, 0, 0, fmt.Errorf("no video stream")
	}

	origWidth := videoStreams[0].Width()
	origHeight := videoStreams[0].Height()
	media.Close()

	// Calculate scaled dimensions (must be even for h264)
	newWidth := (int(float64(origWidth)*scale) / 2) * 2
	newHeight := (int(float64(origHeight)*scale) / 2) * 2

	// Reopen input for transcoding
	inputFile.Seek(0, io.SeekStart)

	// Create ring buffer for streaming
	ringBuf := newRingBuffer()

	// Build scale filter
	filterSpec := fmt.Sprintf("scale=%d:%d", newWidth, newHeight)

	fmt.Printf("Transcoding: %dx%d -> %dx%d (scale=%.0f%%)\n",
		origWidth, origHeight, newWidth, newHeight, scale*100)
	fmt.Printf("Filter: %s\n", filterSpec)
	fmt.Println("Streaming transcoded video...")

	// Channel to signal when enough data is buffered for probing
	ready := make(chan struct{})

	// Start transcoding in background goroutine
	go func() {
		defer inputFile.Close()
		defer ringBuf.Close()

		ws := &pipeWriteSeeker{w: ringBuf}

		// Create and run transcoder using MPEG-TS format for streaming
		tr := reisen.NewTranscoder(inputFile, ws).
			VideoCodec("libx264").
			VideoFilter(filterSpec).
			NoAudio().
			Format("mpegts"). // Use MPEG-TS for streaming compatibility
			OnProgress(func(stats reisen.TranscodeStats) {
				// Signal ready after first few frames are written
				select {
				case <-ready:
				default:
					if stats.FramesProcessed >= 30 {
						close(ready)
					}
				}
				fmt.Printf("\rTranscoding: %.1f%% (%d frames)",
					stats.Progress*100, stats.FramesProcessed)
			})

		err := tr.Run(context.Background())
		if err != nil {
			fmt.Printf("\nTranscode error: %v\n", err)
		} else {
			fmt.Printf("\nTranscoding complete\n")
		}
	}()

	// Wait for enough data to be buffered for probing
	fmt.Println("Waiting for initial frames...")
	select {
	case <-ready:
		fmt.Println("Buffer ready, starting playback...")
	case <-time.After(10 * time.Second):
		return nil, 0, 0, fmt.Errorf("timeout waiting for transcoding to start")
	}

	return ringBuf, newWidth, newHeight, nil
}

// readVideoAndAudio reads video and audio frames
// from the opened media and sends the decoded
// data to the channels to be played.
func readVideoAndAudio(media *reisen.Media, hasAudio bool) (<-chan *image.RGBA, <-chan [2]float64, chan error, error) {
	frameBuffer := make(chan *image.RGBA, frameBufferSize)
	sampleBuffer := make(chan [2]float64, sampleBufferSize)
	errs := make(chan error)

	err := media.OpenDecode()
	if err != nil {
		return nil, nil, nil, err
	}

	videoStream := media.VideoStreams()[0]
	err = videoStream.Open()
	if err != nil {
		return nil, nil, nil, err
	}

	var audioStream *reisen.AudioStream
	if hasAudio && len(media.AudioStreams()) > 0 {
		audioStream = media.AudioStreams()[0]
		err = audioStream.Open()
		if err != nil {
			return nil, nil, nil, err
		}
	}

	go func() {
		for {
			packet, gotPacket, err := media.ReadPacket()

			if err != nil {
				go func(err error) {
					errs <- err
				}(err)
			}

			if !gotPacket {
				break
			}

			switch packet.Type() {
			case reisen.StreamVideo:
				s := media.Streams()[packet.StreamIndex()].(*reisen.VideoStream)
				videoFrame, gotFrame, err := s.ReadVideoFrame()

				if err != nil {
					go func(err error) {
						errs <- err
					}(err)
				}

				if !gotFrame {
					break
				}

				if videoFrame == nil {
					continue
				}

				frameBuffer <- videoFrame.Image()

			case reisen.StreamAudio:
				if audioStream == nil {
					continue
				}
				s := media.Streams()[packet.StreamIndex()].(*reisen.AudioStream)
				audioFrame, gotFrame, err := s.ReadAudioFrame()

				if err != nil {
					go func(err error) {
						errs <- err
					}(err)
				}

				if !gotFrame {
					break
				}

				if audioFrame == nil {
					continue
				}

				// Turn the raw byte data into
				// audio samples of type [2]float64.
				reader := bytes.NewReader(audioFrame.Data())

				for reader.Len() > 0 {
					sample := [2]float64{0, 0}
					var result float64
					err = binary.Read(reader, binary.LittleEndian, &result)

					if err != nil {
						go func(err error) {
							errs <- err
						}(err)
					}

					sample[0] = result

					err = binary.Read(reader, binary.LittleEndian, &result)

					if err != nil {
						go func(err error) {
							errs <- err
						}(err)
					}

					sample[1] = result
					sampleBuffer <- sample
				}
			}
		}

		videoStream.Close()
		if audioStream != nil {
			audioStream.Close()
		}
		media.CloseDecode()
		close(frameBuffer)
		close(sampleBuffer)
		close(errs)
	}()

	return frameBuffer, sampleBuffer, errs, nil
}

// streamSamples creates a new custom streamer for
// playing audio samples provided by the source channel.
func streamSamples(sampleSource <-chan [2]float64) beep.Streamer {
	return beep.StreamerFunc(func(samples [][2]float64) (n int, ok bool) {
		numRead := 0

		for i := 0; i < len(samples); i++ {
			sample, ok := <-sampleSource

			if !ok {
				numRead = i + 1
				break
			}

			samples[i] = sample
			numRead++
		}

		if numRead < len(samples) {
			return numRead, false
		}

		return numRead, true
	})
}

// Game holds all the data
// necessary for playing video.
type Game struct {
	videoSprite            *ebiten.Image
	ticker                 <-chan time.Time
	errs                   <-chan error
	frameBuffer            <-chan *image.RGBA
	fps                    int
	videoTotalFramesPlayed int
	videoPlaybackFPS       int
	perSecond              <-chan time.Time
	last                   time.Time
	deltaTime              float64
	file                   *os.File
	width                  int
	height                 int
	convert                bool
}

// Start reads samples and frames of the media file.
// If convert is true, the file is transcoded using FFmpeg filters via a pipe.
func (game *Game) Start(fname string, convert bool, scale float64) error {
	game.convert = convert

	// Initialize the audio speaker.
	err := speaker.Init(sampleRate, SpeakerSampleRate.N(time.Second/10))
	if err != nil {
		return err
	}

	var media *reisen.Media
	var hasAudio bool

	if convert {
		// Transcode using pipe-based streaming
		fmt.Println("Starting pipe-based transcoding...")

		transcodedReader, width, height, err := transcodeWithPipe(fname, scale)
		if err != nil {
			return fmt.Errorf("transcode: %w", err)
		}

		game.width = width
		game.height = height

		// Create media from the pipe
		media, err = reisen.NewMediaFromReader(transcodedReader)
		if err != nil {
			return fmt.Errorf("open transcoded media: %w", err)
		}

		hasAudio = false // Transcoded without audio

		// Also open original file for potential future audio use
		game.file, err = os.Open(fname)
		if err != nil {
			return err
		}
	} else {
		// Direct playback
		game.file, err = os.Open(fname)
		if err != nil {
			return err
		}

		media, err = reisen.NewMediaFromReader(&limitedReader{r: game.file})
		if err != nil {
			game.file.Close()
			return err
		}

		videoStream := media.VideoStreams()[0]
		game.width = videoStream.Width()
		game.height = videoStream.Height()
		hasAudio = len(media.AudioStreams()) > 0
	}

	// Sprite for drawing video frames.
	game.videoSprite, err = ebiten.NewImage(game.width, game.height, ebiten.FilterDefault)
	if err != nil {
		return err
	}

	// Get the FPS for playing video frames.
	videoFPS, _ := media.VideoStreams()[0].FrameRate()

	// SPF for frame ticker.
	spf := 1.0 / float64(videoFPS)
	frameDuration, err := time.ParseDuration(fmt.Sprintf("%fs", spf))
	if err != nil {
		return err
	}

	// Start decoding streams.
	var sampleSource <-chan [2]float64
	game.frameBuffer, sampleSource, game.errs, err = readVideoAndAudio(media, hasAudio)
	if err != nil {
		return err
	}

	// Start playing audio samples.
	if hasAudio {
		speaker.Play(streamSamples(sampleSource))
	}

	game.ticker = time.Tick(frameDuration)

	// Setup metrics.
	game.last = time.Now()
	game.fps = 0
	game.perSecond = time.Tick(time.Second)
	game.videoTotalFramesPlayed = 0
	game.videoPlaybackFPS = 0

	return nil
}

func (game *Game) Update(screen *ebiten.Image) error {
	// Compute dt.
	game.deltaTime = time.Since(game.last).Seconds()
	game.last = time.Now()

	// Check for incoming errors.
	select {
	case err, ok := <-game.errs:
		if ok {
			return err
		}
	default:
	}

	// Read video frames and draw them.
	select {
	case <-game.ticker:
		frame, ok := <-game.frameBuffer

		if ok {
			game.videoSprite.ReplacePixels(frame.Pix)
			game.videoTotalFramesPlayed++
			game.videoPlaybackFPS++
		}
	default:
	}

	// Draw the video sprite.
	op := &ebiten.DrawImageOptions{}
	err := screen.DrawImage(game.videoSprite, op)
	if err != nil {
		return err
	}

	game.fps++

	// Update metrics in the window title.
	select {
	case <-game.perSecond:
		mode := ""
		if game.convert {
			mode = " [Transcoded]"
		}
		ebiten.SetWindowTitle(fmt.Sprintf("Video%s | FPS: %d | dt: %f | Frames: %d | Video FPS: %d",
			mode, game.fps, game.deltaTime, game.videoTotalFramesPlayed, game.videoPlaybackFPS))

		game.fps = 0
		game.videoPlaybackFPS = 0
	default:
	}

	return nil
}

func (game *Game) Layout(a, b int) (int, int) {
	return game.width, game.height
}

func main() {
	flag.Parse()

	filename := "demo.mp4"
	args := flag.Args()
	if len(args) > 0 {
		filename = args[0]
	}

	game := &Game{}
	err := game.Start(filename, *convertFlag, *scalePercent)
	handleError(err)

	title := "Video"
	if *convertFlag {
		title = fmt.Sprintf("Video [Transcoded: %.0f%% scale]", *scalePercent*100)
	}

	ebiten.SetWindowSize(game.width, game.height)
	ebiten.SetWindowTitle(title)
	err = ebiten.RunGame(game)
	handleError(err)
}

func handleError(err error) {
	if err != nil {
		panic(err)
	}
}
