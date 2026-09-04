package stl

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// ErrHeaderAlreadyWritten is returned by [Writer.WriteHeader] if the header
// has already been emitted (either explicitly or implicitly by the first
// [Writer.WriteTriangle] call).
var ErrHeaderAlreadyWritten = errors.New("stl: header already written")

// Writer streams triangles to an STL output. Construct one with
// [NewWriter]; optionally call [Writer.WriteHeader] to set the solid name
// or 80-byte binary header; call [Writer.WriteTriangle] in a loop; then
// [Writer.Close] to flush trailing bytes.
//
// For binary output the writer streams triangle records directly and
// patches the 4-byte triangle count on [Writer.Close], so the underlying
// writer must satisfy [io.WriterAt] OR the writer buffers triangles in
// memory until Close. Use [NewWriterAt] when you can supply an
// [io.WriterAt] (e.g. an [*os.File]) — it streams triangles to disk with
// O(1) memory. [NewWriter] keeps the API simple by buffering, which is
// fine for ASCII output (no count to patch) but uses memory proportional
// to the triangle count for binary output.
type Writer struct {
	w  io.Writer
	bw *bufio.Writer
	wa io.WriterAt
	// seq is the sequential adapter wrapping wa. It is non-nil exactly
	// when wa is, and lets Close find the offset of the count field.
	seq        *writerAtSequential
	format     Format
	name       string
	binHeader  []byte
	headerDone bool
	closed     bool
	count      uint32
	// binBuf is used by [NewWriter] in binary mode to defer triangle bytes
	// until Close can prepend the count. It is nil when [NewWriterAt] gives
	// us a WriterAt to patch in place.
	binBuf      []byte
	binCountOff int64
}

// NewWriter returns a [Writer] that emits the given [Format] to w.
//
// For [FormatBinary], NewWriter buffers triangle data in memory until
// [Writer.Close] because the 4-byte triangle count must be written before
// the triangle records. If you have a seekable / [io.WriterAt] sink (such
// as an [*os.File]), prefer [NewWriterAt] to stream directly to disk.
func NewWriter(w io.Writer, format Format) *Writer {
	return &Writer{
		w:      w,
		bw:     bufio.NewWriterSize(w, 64*1024),
		format: format,
	}
}

// NewWriterAt returns a [Writer] that streams binary STL directly to wa,
// patching the triangle count in place on [Writer.Close]. For
// [FormatASCII] the WriterAt aspect is irrelevant and behaviour matches
// [NewWriter].
func NewWriterAt(wa io.WriterAt, format Format) *Writer {
	// w is set to a writer that appends sequentially; we track the
	// offset manually via count.
	seq := &writerAtSequential{wa: wa}
	return &Writer{
		w:      seq,
		bw:     nil, // filled in lazily after we know the format
		wa:     wa,
		seq:    seq,
		format: format,
	}
}

// writerAtSequential adapts an io.WriterAt to io.Writer by maintaining its
// own append offset. It is only used internally; callers see [Writer].
type writerAtSequential struct {
	wa  io.WriterAt
	off int64
}

func (s *writerAtSequential) Write(p []byte) (int, error) {
	n, err := s.wa.WriteAt(p, s.off)
	s.off += int64(n)
	return n, err
}

// SetName sets the solid name (ASCII) or seeds the first 80 bytes of the
// binary header. Must be called before any header or triangle is written.
// Long names are truncated to 79 bytes for the binary header to leave room
// for a NUL terminator.
func (w *Writer) SetName(name string) error {
	if w.headerDone {
		return ErrHeaderAlreadyWritten
	}
	w.name = name
	return nil
}

// SetBinaryHeader sets the full 80-byte binary header. Shorter inputs are
// right-padded with NULs; longer inputs are truncated. Has no effect on
// ASCII output. Must be called before [Writer.WriteHeader] or the first
// [Writer.WriteTriangle].
func (w *Writer) SetBinaryHeader(hdr []byte) error {
	if w.headerDone {
		return ErrHeaderAlreadyWritten
	}
	buf := make([]byte, binaryHeaderSize)
	copy(buf, hdr)
	w.binHeader = buf
	return nil
}

// WriteHeader writes the file header. It is optional; [Writer.WriteTriangle]
// calls it implicitly on first use.
func (w *Writer) WriteHeader() error {
	if w.headerDone {
		return nil
	}
	if w.bw == nil {
		// NewWriterAt path: build the bufio.Writer now.
		w.bw = bufio.NewWriterSize(w.w, 64*1024)
	}
	switch w.format {
	case FormatASCII:
		if _, err := fmt.Fprintf(w.bw, "solid %s\n", w.name); err != nil {
			return err
		}
	case FormatBinary:
		if w.binHeader == nil {
			w.binHeader = make([]byte, binaryHeaderSize)
			if w.name != "" {
				copy(w.binHeader, w.name)
			}
		}
		if _, err := w.bw.Write(w.binHeader); err != nil {
			return err
		}
		// Reserve 4 bytes for the count; we will patch it on Close.
		if w.wa != nil {
			// Streaming-to-WriterAt path: remember offset for the patch.
			if err := w.bw.Flush(); err != nil {
				return err
			}
			w.binCountOff = w.seq.off
			w.seq.off += 4
		}
		// In the buffered path we don't write anything else yet; the
		// 4-byte count and accumulated triangles are emitted in Close.
	default:
		return fmt.Errorf("stl: unsupported format %v", w.format)
	}
	w.headerDone = true
	return nil
}

// WriteTriangle appends one triangle to the output.
func (w *Writer) WriteTriangle(t Triangle) error {
	if w.closed {
		return errors.New("stl: write on closed writer")
	}
	if !w.headerDone {
		if err := w.WriteHeader(); err != nil {
			return err
		}
	}
	w.count++
	switch w.format {
	case FormatASCII:
		return writeASCIITriangle(w.bw, t)
	case FormatBinary:
		if w.wa != nil {
			return writeBinaryTriangleTo(w.bw, t)
		}
		// Buffered path: append into binBuf, write at Close.
		var buf [binaryTriangleSize]byte
		encodeBinaryTriangle(buf[:], t)
		w.binBuf = append(w.binBuf, buf[:]...)
		return nil
	default:
		return fmt.Errorf("stl: unsupported format %v", w.format)
	}
}

// Close flushes any pending data. For [FormatBinary] it also writes (or
// patches) the triangle count.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if !w.headerDone {
		if err := w.WriteHeader(); err != nil {
			return err
		}
	}
	switch w.format {
	case FormatASCII:
		if _, err := fmt.Fprintf(w.bw, "endsolid %s\n", w.name); err != nil {
			return err
		}
		return w.bw.Flush()
	case FormatBinary:
		if w.wa != nil {
			if err := w.bw.Flush(); err != nil {
				return err
			}
			var cnt [4]byte
			binary.LittleEndian.PutUint32(cnt[:], w.count)
			_, err := w.wa.WriteAt(cnt[:], w.binCountOff)
			return err
		}
		// Buffered path: emit count then the accumulated triangles.
		var cnt [4]byte
		binary.LittleEndian.PutUint32(cnt[:], w.count)
		if _, err := w.bw.Write(cnt[:]); err != nil {
			return err
		}
		if _, err := w.bw.Write(w.binBuf); err != nil {
			return err
		}
		return w.bw.Flush()
	}
	return nil
}

func encodeBinaryTriangle(b []byte, t Triangle) {
	putF32(b[0:4], t.Normal[0])
	putF32(b[4:8], t.Normal[1])
	putF32(b[8:12], t.Normal[2])
	for i := range 3 {
		off := 12 + i*12
		putF32(b[off:off+4], t.Vertices[i][0])
		putF32(b[off+4:off+8], t.Vertices[i][1])
		putF32(b[off+8:off+12], t.Vertices[i][2])
	}
	binary.LittleEndian.PutUint16(b[48:50], t.Attribute)
}

func putF32(b []byte, f float32) {
	binary.LittleEndian.PutUint32(b, math.Float32bits(f))
}

func writeBinaryTriangleTo(w io.Writer, t Triangle) error {
	var buf [binaryTriangleSize]byte
	encodeBinaryTriangle(buf[:], t)
	_, err := w.Write(buf[:])
	return err
}

// writeASCIITriangle emits one triangle in the canonical ASCII form. The
// formatting matches reference implementations (slic3r, admesh) closely
// enough to round-trip cleanly while remaining diffable.
func writeASCIITriangle(w *bufio.Writer, t Triangle) error {
	if _, err := w.WriteString("  facet normal "); err != nil {
		return err
	}
	if err := writeFloats(w, t.Normal); err != nil {
		return err
	}
	if _, err := w.WriteString("\n    outer loop\n"); err != nil {
		return err
	}
	for i := range 3 {
		if _, err := w.WriteString("      vertex "); err != nil {
			return err
		}
		if err := writeFloats(w, t.Vertices[i]); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	if _, err := w.WriteString("    endloop\n  endfacet\n"); err != nil {
		return err
	}
	return nil
}

func writeFloats(w *bufio.Writer, v Vec3) error {
	var buf [32]byte
	for i, f := range v {
		if i > 0 {
			if err := w.WriteByte(' '); err != nil {
				return err
			}
		}
		s := strconv.AppendFloat(buf[:0], float64(f), 'e', 6, 32)
		if _, err := w.Write(s); err != nil {
			return err
		}
	}
	return nil
}
