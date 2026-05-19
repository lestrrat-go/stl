package stl

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// ErrInvalidFormat is returned when the input cannot be recognized as either
// ASCII or binary STL.
var ErrInvalidFormat = errors.New("stl: invalid format")

// detectPeekSize is the number of bytes the auto-detector peeks at the
// start of the stream. ASCII STL files emit their first ``facet'' keyword
// well before this offset; binary headers can in principle contain any
// bytes, so we look for that keyword rather than relying on the leading
// ``solid'' string (which some binary writers also emit).
const detectPeekSize = 512

// Reader streams triangles from an STL input. Construct one with
// [NewReader]; call [Reader.Header] once if you want the binary header or
// ASCII solid name; then call [Reader.ReadTriangle] in a loop until it
// returns [io.EOF].
type Reader struct {
	br     *bufio.Reader
	format Format

	// headerRead is true once Header has been consumed; ReadTriangle
	// implicitly calls Header on first use.
	headerRead bool
	name       string
	binHeader  []byte

	// Binary state.
	binCount     uint32
	binRead      uint32
	binScratch   [binaryTriangleSize]byte
	binCountKnow bool

	// ASCII state.
	scanner *bufio.Scanner
	// asciiDone is set once we see ``endsolid'' so further calls return EOF
	// cleanly even if trailing whitespace remains.
	asciiDone bool
	// stash + hasStash give the ASCII parser a single-token pushback slot.
	stash    string
	hasStash bool
}

// NewReader returns a [Reader] reading from r. The format is auto-detected
// on the first call to [Reader.Header] or [Reader.ReadTriangle].
func NewReader(r io.Reader) *Reader {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64*1024)
	}
	return &Reader{br: br}
}

// Format returns the detected input format. It is [FormatUnknown] until the
// first call to [Reader.Header] or [Reader.ReadTriangle].
func (r *Reader) Format() Format { return r.format }

// Name returns the ASCII solid name or the trimmed binary header text. It
// is only meaningful after [Reader.Header] (or the first [Reader.ReadTriangle])
// has been called.
func (r *Reader) Name() string { return r.name }

// BinaryHeader returns the raw 80-byte binary header, or nil for ASCII
// input. Only meaningful after [Reader.Header] has been called.
func (r *Reader) BinaryHeader() []byte { return r.binHeader }

// TriangleCount returns the triangle count declared in the binary header,
// and true. For ASCII input it returns (0, false) because ASCII STL does
// not encode a count.
func (r *Reader) TriangleCount() (uint32, bool) {
	if r.format == FormatBinary && r.binCountKnow {
		return r.binCount, true
	}
	return 0, false
}

// Header detects the format and consumes the file header. It is safe to
// call multiple times; subsequent calls are no-ops.
func (r *Reader) Header() error {
	if r.headerRead {
		return nil
	}
	if err := r.detect(); err != nil {
		return err
	}
	switch r.format {
	case FormatBinary:
		if err := r.readBinaryHeader(); err != nil {
			return err
		}
	case FormatASCII:
		if err := r.readASCIIHeader(); err != nil {
			return err
		}
	}
	r.headerRead = true
	return nil
}

// ReadTriangle returns the next triangle, or [io.EOF] when the input is
// exhausted. It implicitly calls [Reader.Header] on first use.
func (r *Reader) ReadTriangle() (Triangle, error) {
	if !r.headerRead {
		if err := r.Header(); err != nil {
			return Triangle{}, err
		}
	}
	switch r.format {
	case FormatBinary:
		return r.readBinaryTriangle()
	case FormatASCII:
		return r.readASCIITriangle()
	default:
		return Triangle{}, ErrInvalidFormat
	}
}

func (r *Reader) detect() error {
	peek, err := r.br.Peek(detectPeekSize)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return err
	}
	if len(peek) == 0 {
		return io.ErrUnexpectedEOF
	}
	// "solid" prefix is not a reliable signal: binary STLs sometimes start
	// with "solid" too. The reliable signal for ASCII is the literal
	// "facet" keyword appearing before any non-printable bytes.
	if bytes.HasPrefix(peek, []byte("solid")) || bytes.HasPrefix(peek, []byte("SOLID")) {
		lower := bytes.ToLower(peek)
		if bytes.Contains(lower, []byte("facet")) {
			r.format = FormatASCII
			return nil
		}
	}
	r.format = FormatBinary
	return nil
}

func (r *Reader) readBinaryHeader() error {
	hdr := make([]byte, binaryHeaderSize)
	if _, err := io.ReadFull(r.br, hdr); err != nil {
		return fmt.Errorf("stl: reading binary header: %w", err)
	}
	r.binHeader = hdr
	r.name = string(bytes.TrimRight(bytes.TrimSpace(hdr), "\x00"))

	var count [4]byte
	if _, err := io.ReadFull(r.br, count[:]); err != nil {
		return fmt.Errorf("stl: reading triangle count: %w", err)
	}
	r.binCount = binary.LittleEndian.Uint32(count[:])
	r.binCountKnow = true
	return nil
}

func (r *Reader) readBinaryTriangle() (Triangle, error) {
	if r.binRead >= r.binCount {
		return Triangle{}, io.EOF
	}
	if _, err := io.ReadFull(r.br, r.binScratch[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Triangle{}, io.ErrUnexpectedEOF
		}
		return Triangle{}, err
	}
	r.binRead++
	return decodeBinaryTriangle(r.binScratch[:]), nil
}

func decodeBinaryTriangle(b []byte) Triangle {
	var t Triangle
	t.Normal = Vec3{
		math.Float32frombits(binary.LittleEndian.Uint32(b[0:4])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[4:8])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[8:12])),
	}
	for i := 0; i < 3; i++ {
		off := 12 + i*12
		t.Vertices[i] = Vec3{
			math.Float32frombits(binary.LittleEndian.Uint32(b[off : off+4])),
			math.Float32frombits(binary.LittleEndian.Uint32(b[off+4 : off+8])),
			math.Float32frombits(binary.LittleEndian.Uint32(b[off+8 : off+12])),
		}
	}
	t.Attribute = binary.LittleEndian.Uint16(b[48:50])
	return t
}

// ----- ASCII parsing -----

func (r *Reader) readASCIIHeader() error {
	r.scanner = bufio.NewScanner(r.br)
	// Some STL files have very long lines (single-line ASCII writers exist);
	// bump the buffer to 1 MiB.
	r.scanner.Buffer(make([]byte, 64*1024), 1<<20)
	r.scanner.Split(bufio.ScanWords)

	tok, err := r.nextToken()
	if err != nil {
		return err
	}
	if !equalFold(tok, "solid") {
		return fmt.Errorf("stl: expected 'solid', got %q", tok)
	}
	// The name runs to end of line. bufio.ScanWords swallows line breaks, so
	// we cannot capture multi-word names without switching strategies. Take
	// the next single word as the name unless it is itself a keyword
	// (``facet'' meaning an unnamed solid).
	tok, err = r.nextToken()
	if err != nil {
		return err
	}
	if equalFold(tok, "facet") {
		// Unnamed solid; replay the token by stashing it.
		r.name = ""
		r.stash = tok
		r.hasStash = true
		return nil
	}
	r.name = tok
	return nil
}

func (r *Reader) readASCIITriangle() (Triangle, error) {
	if r.asciiDone {
		return Triangle{}, io.EOF
	}
	tok, err := r.nextToken()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Triangle{}, io.ErrUnexpectedEOF
		}
		return Triangle{}, err
	}
	if equalFold(tok, "endsolid") {
		r.asciiDone = true
		// Drain any trailing solid name; ignore errors so malformed trailers
		// do not mask the EOF we want to report.
		_, _ = r.nextToken()
		return Triangle{}, io.EOF
	}
	if !equalFold(tok, "facet") {
		return Triangle{}, fmt.Errorf("stl: expected 'facet' or 'endsolid', got %q", tok)
	}

	var t Triangle
	if err := r.expect("normal"); err != nil {
		return Triangle{}, err
	}
	if t.Normal, err = r.readVec3(); err != nil {
		return Triangle{}, err
	}
	if err := r.expect("outer"); err != nil {
		return Triangle{}, err
	}
	if err := r.expect("loop"); err != nil {
		return Triangle{}, err
	}
	for i := 0; i < 3; i++ {
		if err := r.expect("vertex"); err != nil {
			return Triangle{}, err
		}
		v, err := r.readVec3()
		if err != nil {
			return Triangle{}, err
		}
		t.Vertices[i] = v
	}
	if err := r.expect("endloop"); err != nil {
		return Triangle{}, err
	}
	if err := r.expect("endfacet"); err != nil {
		return Triangle{}, err
	}
	return t, nil
}

// stash holds at most one already-read token so the ASCII header parser can
// push back a token it did not consume.
func (r *Reader) nextToken() (string, error) {
	if r.hasStash {
		r.hasStash = false
		return r.stash, nil
	}
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return r.scanner.Text(), nil
}

func (r *Reader) expect(kw string) error {
	tok, err := r.nextToken()
	if err != nil {
		return err
	}
	if !equalFold(tok, kw) {
		return fmt.Errorf("stl: expected %q, got %q", kw, tok)
	}
	return nil
}

func (r *Reader) readVec3() (Vec3, error) {
	var v Vec3
	for i := 0; i < 3; i++ {
		tok, err := r.nextToken()
		if err != nil {
			return Vec3{}, err
		}
		f, err := strconv.ParseFloat(tok, 32)
		if err != nil {
			return Vec3{}, fmt.Errorf("stl: parsing float %q: %w", tok, err)
		}
		v[i] = float32(f)
	}
	return v, nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
