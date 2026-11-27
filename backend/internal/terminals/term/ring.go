package term

import "unicode/utf8"

// RingSize is a terminal's scrollback: the last 256 KiB of output (docs/architecture.md, "Where state lives").
const RingSize = 256 << 10

// Ring keeps the last Size bytes written to it.
type Ring struct {
	buf   []byte
	start int // index of the oldest byte
	n     int // bytes held
	// cut: bytes were dropped, so the oldest byte may sit inside a character or an escape sequence.
	cut bool
}

// NewRing returns a ring of size bytes.
func NewRing(size int) *Ring { return &Ring{buf: make([]byte, size)} }

// Write appends p, dropping the oldest bytes beyond the ring's size.
func (r *Ring) Write(p []byte) {
	size := len(r.buf)
	if size == 0 {
		return
	}
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.start, r.n, r.cut = 0, size, true
		return
	}
	end := (r.start + r.n) % size
	k := copy(r.buf[end:], p)
	copy(r.buf, p[k:])
	r.n += len(p)
	if r.n > size {
		r.start = (r.start + r.n - size) % size
		r.n, r.cut = size, true
	}
}

// Bytes is a copy of what the ring holds, oldest first. After a cut it starts at a character boundary, so a
// replay never opens with half a UTF-8 sequence (an escape sequence cut in half shows as a few stray characters at
// worst; the next prompt redraws the line).
func (r *Ring) Bytes() []byte {
	out := make([]byte, r.n)
	k := copy(out, r.buf[r.start:min(r.start+r.n, len(r.buf))])
	copy(out[k:], r.buf[:r.n-k])
	if r.cut {
		i := 0
		for i < len(out) && i < utf8.UTFMax && !utf8.RuneStart(out[i]) {
			i++
		}
		out = out[i:]
	}
	return out
}

// Len is the number of bytes held.
func (r *Ring) Len() int { return r.n }
