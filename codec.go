package stl

import (
	"errors"
	"io"
)

// Decode reads a complete STL file from r and returns the resulting
// [Solid]. It is a convenience wrapper around [Reader] and loads every
// triangle into memory; for large meshes prefer the streaming [Reader]
// API.
func Decode(r io.Reader) (*Solid, error) {
	rd := NewReader(r)
	if err := rd.Header(); err != nil {
		return nil, err
	}
	s := &Solid{Name: rd.Name(), BinaryHeader: rd.BinaryHeader()}
	if n, ok := rd.TriangleCount(); ok {
		s.Triangles = make([]Triangle, 0, n)
	}
	for {
		t, err := rd.ReadTriangle()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return s, nil
			}
			return nil, err
		}
		s.Triangles = append(s.Triangles, t)
	}
}

// Encode writes s to w in the given [Format]. It is a convenience wrapper
// around [Writer] for callers that already hold the full mesh in memory.
func Encode(w io.Writer, s *Solid, format Format) error {
	wr := NewWriter(w, format)
	if format == FormatBinary && len(s.BinaryHeader) > 0 {
		if err := wr.SetBinaryHeader(s.BinaryHeader); err != nil {
			return err
		}
	}
	if err := wr.SetName(s.Name); err != nil {
		return err
	}
	if err := wr.WriteHeader(); err != nil {
		return err
	}
	for i := range s.Triangles {
		if err := wr.WriteTriangle(s.Triangles[i]); err != nil {
			return err
		}
	}
	return wr.Close()
}
