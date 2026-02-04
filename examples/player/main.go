package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"time"

	"github.com/faiface/beep"
	"github.com/faiface/beep/speaker"
	"github.com/hajimehoshi/ebiten"
	_ "github.com/silbinarywolf/preferdiscretegpu"
	"github.com/zimwip/reisen"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
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
var convertFlag = flag.Bool("convert", false, "Enable on-the-fly conversion (80% scale + text overlay)")
var overlayText = flag.String("text", "Reisen Player", "Text overlay to display when -convert is enabled")

// scaleImage scales an RGBA image by the given factor using nearest-neighbor
func scaleImage(src *image.RGBA, scale float64) *image.RGBA {
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			srcX := int(float64(x) / scale)
			srcY := int(float64(y) / scale)
			if srcX >= srcW {
				srcX = srcW - 1
			}
			if srcY >= srcH {
				srcY = srcH - 1
			}
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}

	return dst
}

// drawTextOverlay draws text on an RGBA image with a semi-transparent background
func drawTextOverlay(img *image.RGBA, text string) {
	// Draw semi-transparent background for text
	bounds := img.Bounds()
	bgHeight := 30
	bgRect := image.Rect(0, bounds.Max.Y-bgHeight, bounds.Max.X, bounds.Max.Y)
	bgColor := color.RGBA{0, 0, 0, 180} // semi-transparent black

	draw.Draw(img, bgRect, &image.Uniform{bgColor}, image.Point{}, draw.Over)

	// Draw text
	textColor := color.RGBA{255, 255, 255, 255} // white
	point := fixed.Point26_6{
		X: fixed.I(10),
		Y: fixed.I(bounds.Max.Y - 10),
	}

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13,
		Dot:  point,
	}
	d.DrawString(text)
}

// processFrame applies transformations to a frame if convert mode is enabled
func processFrame(frame *image.RGBA, convert bool, text string) *image.RGBA {
	if !convert {
		return frame
	}

	// Scale down by 20% (keep 80%)
	scaled := scaleImage(frame, 0.8)

	// Add text overlay
	drawTextOverlay(scaled, text)

	return scaled
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

// readVideoAndAudio reads video and audio frames
// from the opened media and sends the decoded
// data to the channels to be played.
// If convert is true, frames are scaled to 80% and have text overlay applied.
func readVideoAndAudio(media *reisen.Media, convert bool, text string) (<-chan *image.RGBA, <-chan [2]float64, chan error, error) {
	frameBuffer := make(chan *image.RGBA,
		frameBufferSize)
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

	audioStream := media.AudioStreams()[0]
	err = audioStream.Open()

	if err != nil {
		return nil, nil, nil, err
	}

	/*err = media.Streams()[0].Rewind(60 * time.Second)

	if err != nil {
		return nil, nil, nil, err
	}*/

	/*err = media.Streams()[0].ApplyFilter("h264_mp4toannexb")

	if err != nil {
		return nil, nil, nil, err
	}*/

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

			/*hash := sha256.Sum256(packet.Data())
			fmt.Println(base58.Encode(hash[:]))*/

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

				// Apply transformation if convert mode is enabled
				frame := processFrame(videoFrame.Image(), convert, text)
				frameBuffer <- frame

			case reisen.StreamAudio:
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

				// See the README.md file for
				// detailed scheme of the sample structure.
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
		audioStream.Close()
		media.CloseDecode()
		close(frameBuffer)
		close(sampleBuffer)
		close(errs)
	}()

	return frameBuffer, sampleBuffer, errs, nil
}

// streamSamples creates a new custom streamer for
// playing audio samples provided by the source channel.
//
// See https://github.com/faiface/beep/wiki/Making-own-streamers
// for reference.
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
	overlayText            string
}

// Start reads samples and frames of the media file.
// If convert is true, frames are scaled to 80% with text overlay.
func (game *Game) Start(fname string, convert bool, text string) error {
	game.convert = convert
	game.overlayText = text

	// Initialize the audio speaker.
	err := speaker.Init(sampleRate,
		SpeakerSampleRate.N(time.Second/10))

	if err != nil {
		return err
	}

	// Open the media file.
	game.file, err = os.Open(fname)

	if err != nil {
		return err
	}

	media, err := reisen.NewMediaFromReader(&limitedReader{r: game.file})

	if err != nil {
		game.file.Close()
		return err
	}

	// Get video stream info.
	videoStream := media.VideoStreams()[0]
	game.width = videoStream.Width()
	game.height = videoStream.Height()

	// If convert mode, adjust dimensions to 80%
	if convert {
		game.width = int(float64(game.width) * 0.8)
		game.height = int(float64(game.height) * 0.8)
	}

	// Sprite for drawing video frames.
	game.videoSprite, err = ebiten.NewImage(
		game.width, game.height, ebiten.FilterDefault)

	if err != nil {
		return err
	}

	// Get the FPS for playing video frames.
	videoFPS, _ := videoStream.FrameRate()

	if err != nil {
		return err
	}

	// SPF for frame ticker.
	spf := 1.0 / float64(videoFPS)
	frameDuration, err := time.
		ParseDuration(fmt.Sprintf("%fs", spf))

	if err != nil {
		return err
	}

	// Start decoding streams.
	var sampleSource <-chan [2]float64
	game.frameBuffer, sampleSource,
		game.errs, err = readVideoAndAudio(media, convert, text)

	if err != nil {
		return err
	}

	// Start playing audio samples.
	speaker.Play(streamSamples(sampleSource))

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
		ebiten.SetWindowTitle(fmt.Sprintf("%s | FPS: %d | dt: %f | Frames: %d | Video FPS: %d",
			"Video", game.fps, game.deltaTime, game.videoTotalFramesPlayed, game.videoPlaybackFPS))

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
	err := game.Start(filename, *convertFlag, *overlayText)
	handleError(err)

	title := "Video"
	if *convertFlag {
		title = fmt.Sprintf("Video [Converting: 80%% scale + \"%s\"]", *overlayText)
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
