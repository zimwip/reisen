package reisen

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// readWriteSeeker combines read, write, and seek capabilities.
type readWriteSeeker interface {
	io.Reader
	io.Writer
	io.Seeker
}

// mp4Box describes a top-level MP4 box.
type mp4Box struct {
	offset int64
	size   int64
	typ    string
}

// stripFaststart removes "faststart" from a +-separated movflags string.
// Returns the cleaned string and whether faststart was present.
func stripFaststart(movflags string) (string, bool) {
	parts := strings.Split(movflags, "+")
	var kept []string
	found := false
	for _, p := range parts {
		if p == "faststart" {
			found = true
		} else if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "+"), found
}

// doFaststart relocates the moov atom for fast-start playback.
func (t *Transcoder) doFaststart() error {
	rws, ok := t.output.(readWriteSeeker)
	if !ok {
		return fmt.Errorf("output must support reading for faststart (e.g., *os.File)")
	}
	return relocateMoov(rws)
}

// relocateMoov moves the moov atom before mdat for fast-start playback.
// This replaces FFmpeg's built-in +faststart which requires re-opening the
// output file by URL — impossible with custom AVIO contexts.
func relocateMoov(rws readWriteSeeker) error {
	boxes, err := parseTopLevelBoxes(rws)
	if err != nil {
		return err
	}

	var moov, mdat *mp4Box
	for i := range boxes {
		switch boxes[i].typ {
		case "moov":
			moov = &boxes[i]
		case "mdat":
			if mdat == nil {
				mdat = &boxes[i]
			}
		}
	}

	if moov == nil {
		return fmt.Errorf("no moov atom found")
	}
	if mdat == nil {
		return fmt.Errorf("no mdat atom found")
	}

	// Already fast-start
	if moov.offset < mdat.offset {
		return nil
	}

	// Read entire moov atom into memory
	moovData := make([]byte, moov.size)
	if _, err := rws.Seek(moov.offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.ReadFull(rws, moovData); err != nil {
		return err
	}

	// Adjust chunk offsets: mdat will shift forward by moov.size
	if err := adjustChunkOffsets(moovData, moov.size); err != nil {
		return err
	}

	// Shift data in [mdat.offset, moov.offset) forward by moov.size
	if err := shiftForward(rws, mdat.offset, moov.offset, moov.size); err != nil {
		return err
	}

	// Write moov at the old mdat position
	if _, err := rws.Seek(mdat.offset, io.SeekStart); err != nil {
		return err
	}
	_, err = rws.Write(moovData)
	return err
}

// parseTopLevelBoxes reads the top-level MP4 box structure.
func parseTopLevelBoxes(rs io.ReadSeeker) ([]mp4Box, error) {
	fileSize, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	var boxes []mp4Box
	var hdr [16]byte
	pos := int64(0)

	for pos < fileSize {
		if _, err := rs.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(rs, hdr[:8]); err != nil {
			break
		}

		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])

		if size == 1 {
			// Extended 64-bit size
			if _, err := io.ReadFull(rs, hdr[8:16]); err != nil {
				break
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		} else if size == 0 {
			// Box extends to end of file
			size = fileSize - pos
		}

		if size < 8 {
			break
		}

		boxes = append(boxes, mp4Box{offset: pos, size: size, typ: typ})
		pos += size
	}

	return boxes, nil
}

// adjustChunkOffsets updates stco/co64 entries inside moov by delta bytes.
func adjustChunkOffsets(moovData []byte, delta int64) error {
	if len(moovData) < 8 {
		return fmt.Errorf("moov too short")
	}
	// Skip moov box header (8 bytes: size + type)
	return walkAtoms(moovData[8:], delta)
}

func walkAtoms(data []byte, delta int64) error {
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		typ := string(data[pos+4 : pos+8])

		hdrSize := 8
		if size == 1 && pos+16 <= len(data) {
			size = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			hdrSize = 16
		} else if size == 0 {
			size = len(data) - pos
		}

		if size < hdrSize || pos+size > len(data) {
			break
		}

		body := data[pos+hdrSize : pos+size]

		switch {
		case isContainerAtom(typ):
			if err := walkAtoms(body, delta); err != nil {
				return err
			}
		case typ == "stco":
			patchStco(body, delta)
		case typ == "co64":
			patchCo64(body, delta)
		}

		pos += size
	}
	return nil
}

func isContainerAtom(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "udta", "mvex":
		return true
	}
	return false
}

// patchStco adjusts uint32 chunk offsets in an stco atom body.
// Body layout: version(1) + flags(3) + entry_count(4) + entries(4 each)
func patchStco(body []byte, delta int64) {
	if len(body) < 8 {
		return
	}
	n := binary.BigEndian.Uint32(body[4:8])
	for i := uint32(0); i < n; i++ {
		off := 8 + i*4
		if int(off+4) > len(body) {
			break
		}
		v := int64(binary.BigEndian.Uint32(body[off : off+4]))
		binary.BigEndian.PutUint32(body[off:off+4], uint32(v+delta))
	}
}

// patchCo64 adjusts uint64 chunk offsets in a co64 atom body.
// Body layout: version(1) + flags(3) + entry_count(4) + entries(8 each)
func patchCo64(body []byte, delta int64) {
	if len(body) < 8 {
		return
	}
	n := binary.BigEndian.Uint32(body[4:8])
	for i := uint32(0); i < n; i++ {
		off := 8 + i*8
		if int(off+8) > len(body) {
			break
		}
		v := int64(binary.BigEndian.Uint64(body[off : off+8]))
		binary.BigEndian.PutUint64(body[off:off+8], uint64(v+delta))
	}
}

// shiftForward copies bytes in [from, to) forward by dist bytes.
// Works backwards to handle overlapping source and destination.
func shiftForward(rws readWriteSeeker, from, to, dist int64) error {
	const chunk = 1 << 20 // 1 MiB
	buf := make([]byte, chunk)
	rem := to - from

	for rem > 0 {
		n := int64(chunk)
		if n > rem {
			n = rem
		}
		src := from + rem - n

		if _, err := rws.Seek(src, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(rws, buf[:n]); err != nil {
			return err
		}
		if _, err := rws.Seek(src+dist, io.SeekStart); err != nil {
			return err
		}
		if _, err := rws.Write(buf[:n]); err != nil {
			return err
		}

		rem -= n
	}
	return nil
}
