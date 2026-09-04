// Package stl reads and writes Stereolithography (STL) files in both ASCII
// and binary formats.
//
// The primary API is streaming: [Reader] yields one [Triangle] at a time so
// large meshes do not need to be held in memory, and [Writer] consumes one
// triangle at a time. [Decode] and [Encode] are convenience wrappers around
// the streaming API that operate on a whole [Solid].
package stl

import "math"

// Format identifies an STL serialization variant.
type Format int

const (
	// FormatUnknown is the zero value; it is returned by [Reader.Format]
	// before the first call that probes the input.
	FormatUnknown Format = iota
	// FormatASCII is the human-readable ``solid ... endsolid'' form.
	FormatASCII
	// FormatBinary is the 80-byte-header + uint32-count + 50-byte-triangle
	// little-endian form.
	FormatBinary
)

// String returns "ascii", "binary", or "unknown".
func (f Format) String() string {
	switch f {
	case FormatASCII:
		return "ascii"
	case FormatBinary:
		return "binary"
	default:
		return "unknown"
	}
}

// Vec3 is a 3-component single-precision vector. STL stores all coordinates
// as IEEE-754 float32 little-endian, so float32 is used directly to avoid
// per-triangle conversions on the hot path.
type Vec3 [3]float32

// Triangle is one facet of an STL mesh.
//
// Attribute is the per-triangle “attribute byte count” field from the
// binary format. The STL specification defines it as zero, but some tools
// (notably colored-STL extensions) store data there, so it is preserved on
// read and emitted on write. It is always zero for ASCII input.
type Triangle struct {
	Normal    Vec3
	Vertices  [3]Vec3
	Attribute uint16
}

// ComputedNormal returns the unit normal derived from the triangle's
// vertices using the right-hand rule. It is useful when a file's stored
// normals are zero or unreliable.
func (t Triangle) ComputedNormal() Vec3 {
	ax := t.Vertices[1][0] - t.Vertices[0][0]
	ay := t.Vertices[1][1] - t.Vertices[0][1]
	az := t.Vertices[1][2] - t.Vertices[0][2]
	bx := t.Vertices[2][0] - t.Vertices[0][0]
	by := t.Vertices[2][1] - t.Vertices[0][1]
	bz := t.Vertices[2][2] - t.Vertices[0][2]
	nx := ay*bz - az*by
	ny := az*bx - ax*bz
	nz := ax*by - ay*bx
	l := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	if l == 0 {
		return Vec3{0, 0, 0}
	}
	return Vec3{nx / l, ny / l, nz / l}
}

// Solid is the in-memory representation of an STL file. It is produced by
// [Decode] and consumed by [Encode].
type Solid struct {
	// Name is the ASCII solid name (from `solid <name>`). For binary input
	// it is derived from the 80-byte header by trimming trailing NULs and
	// whitespace.
	Name string
	// BinaryHeader is the raw 80-byte header from a binary file. It is
	// preserved so that round-trips do not lose vendor metadata. Empty for
	// ASCII input.
	BinaryHeader []byte
	Triangles    []Triangle
}

// binaryHeaderSize is the fixed 80-byte header at the start of every binary
// STL file.
const binaryHeaderSize = 80

// binaryTriangleSize is the on-disk size of one triangle record in the
// binary format: 12 float32 coordinates (48 bytes) + 1 uint16 attribute.
const binaryTriangleSize = 50
