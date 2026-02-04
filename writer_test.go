package reisen

import (
	"bytes"
	"testing"
)

func TestWriterRegistry(t *testing.T) {
	buf := &bytes.Buffer{}
	ws := &bufferWriteSeeker{buf: buf}

	// Register
	id := registerWriter(ws)
	if id == 0 {
		t.Error("expected non-zero ID")
	}

	// Retrieve
	ctx := getWriterContext(id)
	if ctx == nil {
		t.Error("expected to retrieve writer context")
	}
	if ctx.writer != ws {
		t.Error("writer mismatch")
	}

	// Unregister
	unregisterWriter(id)
	ctx = getWriterContext(id)
	if ctx != nil {
		t.Error("expected nil after unregister")
	}
}

// bufferWriteSeeker wraps bytes.Buffer with seeking support
type bufferWriteSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func (b *bufferWriteSeeker) Write(p []byte) (n int, err error) {
	// Extend buffer if needed
	if int(b.pos) > b.buf.Len() {
		b.buf.Write(make([]byte, int(b.pos)-b.buf.Len()))
	}
	// Write at current position
	data := b.buf.Bytes()
	n = copy(data[b.pos:], p)
	if n < len(p) {
		b.buf.Write(p[n:])
		n = len(p)
	}
	b.pos += int64(n)
	return n, nil
}

func (b *bufferWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case 0: // io.SeekStart
		newPos = offset
	case 1: // io.SeekCurrent
		newPos = b.pos + offset
	case 2: // io.SeekEnd
		newPos = int64(b.buf.Len()) + offset
	}
	b.pos = newPos
	return b.pos, nil
}
