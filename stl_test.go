package stl_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/stl"
	"github.com/stretchr/testify/require"
)

// sampleSolid returns a small mesh used across most tests. The triangles
// form a simple tetrahedron-ish shape; exact geometry does not matter as
// long as the values round-trip.
func sampleSolid() *stl.Solid {
	return &stl.Solid{
		Name: "test",
		Triangles: []stl.Triangle{
			{
				Normal:   stl.Vec3{0, 0, 1},
				Vertices: [3]stl.Vec3{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
			},
			{
				Normal:   stl.Vec3{1, 0, 0},
				Vertices: [3]stl.Vec3{{0, 0, 0}, {0, 1, 0}, {0, 0, 1}},
			},
			{
				Normal:    stl.Vec3{0, 1, 0},
				Vertices:  [3]stl.Vec3{{0, 0, 0}, {0, 0, 1}, {1, 0, 0}},
				Attribute: 0, // ASCII can't preserve attribute, so leave zero
			},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	src := sampleSolid()

	for _, tc := range []struct {
		name   string
		format stl.Format
	}{
		{"ascii", stl.FormatASCII},
		{"binary", stl.FormatBinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			require.NoError(t, stl.Encode(&buf, src, tc.format))

			got, err := stl.Decode(&buf)
			require.NoError(t, err)
			require.Equal(t, src.Name, got.Name)
			require.Equal(t, len(src.Triangles), len(got.Triangles))
			for i := range src.Triangles {
				require.Equal(t, src.Triangles[i].Normal, got.Triangles[i].Normal, "tri %d normal", i)
				require.Equal(t, src.Triangles[i].Vertices, got.Triangles[i].Vertices, "tri %d vertices", i)
			}
		})
	}
}

func TestBinaryAttributePreserved(t *testing.T) {
	t.Parallel()
	src := &stl.Solid{
		Name: "attr",
		Triangles: []stl.Triangle{{
			Normal:    stl.Vec3{0, 0, 1},
			Vertices:  [3]stl.Vec3{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
			Attribute: 0xABCD,
		}},
	}
	var buf bytes.Buffer
	require.NoError(t, stl.Encode(&buf, src, stl.FormatBinary))
	got, err := stl.Decode(&buf)
	require.NoError(t, err)
	require.Equal(t, uint16(0xABCD), got.Triangles[0].Attribute)
}

func TestBinaryHeaderPreserved(t *testing.T) {
	t.Parallel()
	src := &stl.Solid{
		Name:         "hdr",
		BinaryHeader: append([]byte("vendor=acme; tool=blender"), bytes.Repeat([]byte{0}, 80-len("vendor=acme; tool=blender"))...),
		Triangles: []stl.Triangle{{
			Normal:   stl.Vec3{0, 0, 1},
			Vertices: [3]stl.Vec3{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
		}},
	}
	var buf bytes.Buffer
	require.NoError(t, stl.Encode(&buf, src, stl.FormatBinary))
	got, err := stl.Decode(&buf)
	require.NoError(t, err)
	require.Equal(t, src.BinaryHeader, got.BinaryHeader)
}

func TestAutoDetectASCII(t *testing.T) {
	t.Parallel()
	const input = `solid foo
  facet normal 0 0 1
    outer loop
      vertex 0 0 0
      vertex 1 0 0
      vertex 0 1 0
    endloop
  endfacet
endsolid foo
`
	r := stl.NewReader(strings.NewReader(input))
	require.NoError(t, r.Header())
	require.Equal(t, stl.FormatASCII, r.Format())
	require.Equal(t, "foo", r.Name())

	tri, err := r.ReadTriangle()
	require.NoError(t, err)
	require.Equal(t, stl.Vec3{0, 0, 1}, tri.Normal)

	_, err = r.ReadTriangle()
	require.ErrorIs(t, err, io.EOF)
}

// TestAutoDetectBinaryWithSolidPrefix exercises the worst case for
// auto-detection: a binary file whose 80-byte header begins with "solid".
// Real-world binary writers do this; libraries that key off the leading
// "solid" alone misclassify them.
func TestAutoDetectBinaryWithSolidPrefix(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	hdr := make([]byte, 80)
	copy(hdr, "solid produced-by-CAD-vendor-X")
	buf.Write(hdr)
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], 1)
	buf.Write(cnt[:])
	var tri [50]byte
	binary.LittleEndian.PutUint32(tri[0:4], math.Float32bits(0))
	binary.LittleEndian.PutUint32(tri[4:8], math.Float32bits(0))
	binary.LittleEndian.PutUint32(tri[8:12], math.Float32bits(1))
	buf.Write(tri[:])

	r := stl.NewReader(&buf)
	require.NoError(t, r.Header())
	require.Equal(t, stl.FormatBinary, r.Format())
	got, err := r.ReadTriangle()
	require.NoError(t, err)
	require.Equal(t, stl.Vec3{0, 0, 1}, got.Normal)
}

func TestStreamingReaderCount(t *testing.T) {
	t.Parallel()
	src := sampleSolid()
	var buf bytes.Buffer
	require.NoError(t, stl.Encode(&buf, src, stl.FormatBinary))

	r := stl.NewReader(&buf)
	require.NoError(t, r.Header())
	count, ok := r.TriangleCount()
	require.True(t, ok)
	require.Equal(t, uint32(len(src.Triangles)), count)
}

func TestStreamingWriterAt(t *testing.T) {
	t.Parallel()
	src := sampleSolid()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.stl")
	f, err := os.Create(path)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	w := stl.NewWriterAt(f, stl.FormatBinary)
	require.NoError(t, w.SetName(src.Name))
	require.NoError(t, w.WriteHeader())
	for _, tri := range src.Triangles {
		require.NoError(t, w.WriteTriangle(tri))
	}
	require.NoError(t, w.Close())

	// Read it back.
	rf, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { rf.Close() })
	got, err := stl.Decode(rf)
	require.NoError(t, err)
	require.Equal(t, len(src.Triangles), len(got.Triangles))
	for i := range src.Triangles {
		require.Equal(t, src.Triangles[i].Normal, got.Triangles[i].Normal)
		require.Equal(t, src.Triangles[i].Vertices, got.Triangles[i].Vertices)
	}
}

func TestMalformedASCII(t *testing.T) {
	t.Parallel()
	const broken = "solid x\n  facet normal 0 0 1\n    outer loop\n      vertex nope 0 0\n"
	_, err := stl.Decode(strings.NewReader(broken))
	require.Error(t, err)
}

func TestTruncatedBinary(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	hdr := make([]byte, 80)
	buf.Write(hdr)
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], 5) // claim 5 triangles
	buf.Write(cnt[:])
	// supply only one triangle's worth of bytes
	buf.Write(make([]byte, 50))

	_, err := stl.Decode(&buf)
	require.Error(t, err)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestComputedNormal(t *testing.T) {
	t.Parallel()
	tri := stl.Triangle{
		Vertices: [3]stl.Vec3{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
	}
	got := tri.ComputedNormal()
	require.InDelta(t, 0, got[0], 1e-6)
	require.InDelta(t, 0, got[1], 1e-6)
	require.InDelta(t, 1, got[2], 1e-6)
}

func TestUnnamedASCIISolid(t *testing.T) {
	t.Parallel()
	const input = `solid
  facet normal 0 0 1
    outer loop
      vertex 0 0 0
      vertex 1 0 0
      vertex 0 1 0
    endloop
  endfacet
endsolid
`
	got, err := stl.Decode(strings.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, "", got.Name)
	require.Len(t, got.Triangles, 1)
}
