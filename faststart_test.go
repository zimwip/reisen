package reisen

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// bytesRWS is a read-write-seeker backed by a byte slice, for testing.
type bytesRWS struct {
	data []byte
	pos  int
}

func newBytesRWS(data []byte) *bytesRWS {
	cp := make([]byte, len(data))
	copy(cp, data)
	return &bytesRWS{data: cp}
}

func (b *bytesRWS) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}

func (b *bytesRWS) Write(p []byte) (int, error) {
	needed := b.pos + len(p)
	if needed > len(b.data) {
		b.data = append(b.data, make([]byte, needed-len(b.data))...)
	}
	copy(b.data[b.pos:], p)
	b.pos += len(p)
	return len(p), nil
}

func (b *bytesRWS) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = int64(b.pos) + offset
	case io.SeekEnd:
		newPos = int64(len(b.data)) + offset
	}
	if newPos < 0 {
		return 0, fmt.Errorf("negative seek position")
	}
	b.pos = int(newPos)
	return newPos, nil
}

// makeBox builds a raw MP4 box: [size:4][type:4][payload...]
func makeBox(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], size)
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

// makeStco builds a stco atom body: version/flags(4) + count(4) + offsets(4 each)
func makeStco(offsets []uint32) []byte {
	body := make([]byte, 8+len(offsets)*4)
	// version=0, flags=0 (zeroed)
	binary.BigEndian.PutUint32(body[4:8], uint32(len(offsets)))
	for i, off := range offsets {
		binary.BigEndian.PutUint32(body[8+i*4:12+i*4], off)
	}
	return makeBox("stco", body)
}

// makeCo64 builds a co64 atom body with uint64 offsets.
func makeCo64(offsets []uint64) []byte {
	body := make([]byte, 8+len(offsets)*8)
	binary.BigEndian.PutUint32(body[4:8], uint32(len(offsets)))
	for i, off := range offsets {
		binary.BigEndian.PutUint64(body[8+i*8:16+i*8], off)
	}
	return makeBox("co64", body)
}

// buildSyntheticMP4 constructs a minimal MP4 with ftyp, mdat, moov (in that order).
// The stco entry points to the start of mdat data.
func buildSyntheticMP4(useStco bool) ([]byte, uint32) {
	ftyp := makeBox("ftyp", []byte("isom\x00\x00\x00\x00"))

	mdatPayload := make([]byte, 100)
	for i := range mdatPayload {
		mdatPayload[i] = 0xAB
	}
	mdat := makeBox("mdat", mdatPayload)

	// Chunk offset points to start of mdat data (after ftyp + mdat header)
	chunkOffset := uint32(len(ftyp) + 8) // ftyp.size + mdat header(8)

	var chunkAtom []byte
	if useStco {
		chunkAtom = makeStco([]uint32{chunkOffset})
	} else {
		chunkAtom = makeCo64([]uint64{uint64(chunkOffset)})
	}

	stbl := makeBox("stbl", chunkAtom)
	minf := makeBox("minf", stbl)
	mdia := makeBox("mdia", minf)
	trak := makeBox("trak", mdia)
	moov := makeBox("moov", trak)

	var buf bytes.Buffer
	buf.Write(ftyp)
	buf.Write(mdat)
	buf.Write(moov)

	return buf.Bytes(), chunkOffset
}

func TestStripFaststart(t *testing.T) {
	tests := []struct {
		input    string
		want     string
		wantFlag bool
	}{
		{"faststart", "", true},
		{"+faststart", "", true},
		{"frag_keyframe+faststart", "frag_keyframe", true},
		{"+faststart+frag_keyframe", "frag_keyframe", true},
		{"frag_keyframe+empty_moov+faststart", "frag_keyframe+empty_moov", true},
		{"frag_keyframe", "frag_keyframe", false},
		{"frag_keyframe+empty_moov", "frag_keyframe+empty_moov", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, gotFlag := stripFaststart(tt.input)
		if got != tt.want || gotFlag != tt.wantFlag {
			t.Errorf("stripFaststart(%q) = (%q, %v), want (%q, %v)",
				tt.input, got, gotFlag, tt.want, tt.wantFlag)
		}
	}
}

func TestRelocateMoovStco(t *testing.T) {
	data, origOffset := buildSyntheticMP4(true)
	rws := newBytesRWS(data)

	// Verify moov is after mdat before relocation
	boxesBefore, err := parseTopLevelBoxes(rws)
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}
	var moovBefore, mdatBefore *mp4Box
	for i := range boxesBefore {
		switch boxesBefore[i].typ {
		case "moov":
			moovBefore = &boxesBefore[i]
		case "mdat":
			mdatBefore = &boxesBefore[i]
		}
	}
	if moovBefore.offset < mdatBefore.offset {
		t.Fatal("expected moov after mdat in test data")
	}

	moovSize := moovBefore.size

	// Relocate
	if err := relocateMoov(rws); err != nil {
		t.Fatalf("relocateMoov: %v", err)
	}

	// Verify moov is now before mdat
	boxesAfter, err := parseTopLevelBoxes(rws)
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}

	var moovAfter, mdatAfter *mp4Box
	for i := range boxesAfter {
		switch boxesAfter[i].typ {
		case "moov":
			moovAfter = &boxesAfter[i]
		case "mdat":
			mdatAfter = &boxesAfter[i]
		}
	}

	if moovAfter.offset >= mdatAfter.offset {
		t.Errorf("moov (offset=%d) should be before mdat (offset=%d)", moovAfter.offset, mdatAfter.offset)
	}

	// Verify chunk offset was adjusted by moov size
	expectedOffset := origOffset + uint32(moovSize)
	// Read the stco entry from relocated moov
	rws.Seek(moovAfter.offset, io.SeekStart)
	moovData := make([]byte, moovAfter.size)
	io.ReadFull(rws, moovData)

	stcoOffset := findStcoOffset(moovData)
	if stcoOffset != expectedOffset {
		t.Errorf("stco offset = %d, want %d (original %d + moov size %d)",
			stcoOffset, expectedOffset, origOffset, moovSize)
	}

	// Verify mdat data is intact
	rws.Seek(mdatAfter.offset+8, io.SeekStart) // skip mdat header
	mdatData := make([]byte, 100)
	io.ReadFull(rws, mdatData)
	for i, b := range mdatData {
		if b != 0xAB {
			t.Errorf("mdat data[%d] = 0x%02X, want 0xAB", i, b)
			break
		}
	}

	// Verify total size unchanged
	if len(rws.data) != len(data) {
		t.Errorf("file size changed: %d -> %d", len(data), len(rws.data))
	}
}

func TestRelocateMoovCo64(t *testing.T) {
	data, origOffset := buildSyntheticMP4(false)
	rws := newBytesRWS(data)

	boxesBefore, _ := parseTopLevelBoxes(rws)
	var moovSize int64
	for _, b := range boxesBefore {
		if b.typ == "moov" {
			moovSize = b.size
		}
	}

	if err := relocateMoov(rws); err != nil {
		t.Fatalf("relocateMoov: %v", err)
	}

	boxesAfter, _ := parseTopLevelBoxes(rws)
	var moovAfter, mdatAfter *mp4Box
	for i := range boxesAfter {
		switch boxesAfter[i].typ {
		case "moov":
			moovAfter = &boxesAfter[i]
		case "mdat":
			mdatAfter = &boxesAfter[i]
		}
	}

	if moovAfter.offset >= mdatAfter.offset {
		t.Errorf("moov should be before mdat after relocation")
	}

	// Read co64 entry
	rws.Seek(moovAfter.offset, io.SeekStart)
	moovData := make([]byte, moovAfter.size)
	io.ReadFull(rws, moovData)

	co64Offset := findCo64Offset(moovData)
	expectedOffset := uint64(origOffset) + uint64(moovSize)
	if co64Offset != expectedOffset {
		t.Errorf("co64 offset = %d, want %d", co64Offset, expectedOffset)
	}
}

func TestRelocateMoovAlreadyFaststart(t *testing.T) {
	// Build MP4 with moov before mdat
	ftyp := makeBox("ftyp", []byte("isom\x00\x00\x00\x00"))
	stco := makeStco([]uint32{200})
	stbl := makeBox("stbl", stco)
	minf := makeBox("minf", stbl)
	mdia := makeBox("mdia", minf)
	trak := makeBox("trak", mdia)
	moov := makeBox("moov", trak)
	mdat := makeBox("mdat", make([]byte, 100))

	var buf bytes.Buffer
	buf.Write(ftyp)
	buf.Write(moov) // moov before mdat
	buf.Write(mdat)

	original := make([]byte, buf.Len())
	copy(original, buf.Bytes())

	rws := newBytesRWS(buf.Bytes())
	if err := relocateMoov(rws); err != nil {
		t.Fatalf("relocateMoov: %v", err)
	}

	// Data should be unchanged
	if !bytes.Equal(rws.data, original) {
		t.Error("data was modified when moov was already before mdat")
	}
}

func TestTranscoderFaststartFlag(t *testing.T) {
	var output bytes.Buffer
	ws := &bufferWriteSeeker{buf: &output}

	tr := NewTranscoder(nil, ws).
		VideoCodec("libx264").
		Format("mp4").
		FormatOption("movflags", "frag_keyframe+faststart")

	// setupOutput triggers the faststart stripping
	// We can't call it without a full context, so test the strip directly
	cleaned, found := stripFaststart(tr.formatOpts["movflags"])
	if !found {
		t.Error("expected faststart to be found in movflags")
	}
	if cleaned != "frag_keyframe" {
		t.Errorf("expected 'frag_keyframe', got %q", cleaned)
	}
}

func TestFaststartIntegration(t *testing.T) {
	filePath := "examples/player/demo.mp4"
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Skip("test file not found")
	}

	inputFile, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open input: %v", err)
	}
	defer inputFile.Close()

	// Use a temp file as output so it supports read+write+seek
	outputFile, err := os.CreateTemp("", "reisen-faststart-*.mp4")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(outputFile.Name())
	defer outputFile.Close()

	tr := NewTranscoder(inputFile, outputFile).
		VideoCodec("libx264").
		VideoFilter("scale=320:-2").
		NoAudio().
		Format("mp4").
		FormatOption("movflags", "+faststart").
		Duration(2 * time.Second)

	err = tr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify output is non-empty
	info, err := outputFile.Stat()
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty")
	}
	t.Logf("output size: %d bytes", info.Size())

	// Parse top-level boxes and verify moov is before mdat
	boxes, err := parseTopLevelBoxes(outputFile)
	if err != nil {
		t.Fatalf("parse output boxes: %v", err)
	}

	var moovOffset, mdatOffset int64
	moovOffset = -1
	mdatOffset = -1
	for _, b := range boxes {
		t.Logf("box: %s offset=%d size=%d", b.typ, b.offset, b.size)
		switch b.typ {
		case "moov":
			moovOffset = b.offset
		case "mdat":
			mdatOffset = b.offset
		}
	}

	if moovOffset < 0 {
		t.Fatal("no moov atom found in output")
	}
	if mdatOffset < 0 {
		t.Fatal("no mdat atom found in output")
	}
	if moovOffset >= mdatOffset {
		t.Errorf("moov (offset=%d) is not before mdat (offset=%d) — faststart failed",
			moovOffset, mdatOffset)
	}
}

// findStcoOffset extracts the first stco chunk offset from moov data.
func findStcoOffset(moovData []byte) uint32 {
	idx := bytes.Index(moovData, []byte("stco"))
	if idx < 0 {
		return 0
	}
	// stco header at idx-4, body starts at idx+4
	body := moovData[idx+4:]
	if len(body) < 12 {
		return 0
	}
	return binary.BigEndian.Uint32(body[8:12])
}

// findCo64Offset extracts the first co64 chunk offset from moov data.
func findCo64Offset(moovData []byte) uint64 {
	idx := bytes.Index(moovData, []byte("co64"))
	if idx < 0 {
		return 0
	}
	body := moovData[idx+4:]
	if len(body) < 16 {
		return 0
	}
	return binary.BigEndian.Uint64(body[8:16])
}
