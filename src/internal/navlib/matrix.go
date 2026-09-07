package navlib

import "fmt"

// Layout describes where a 4x4 affine transform keeps its translation within
// the flat 16-element array exchanged over the wire.
//
// This differs by client library version and getting it wrong silently
// corrupts navigation. See docs/03-client-library-versions.md.
type Layout int

const (
	// LayoutColumnMajor puts translation at flat indices 12,13,14.
	// This is 3DconnexionJS >= 0.5 default, current Onshape, and what
	// THREE.js Matrix4.toArray() produces.
	LayoutColumnMajor Layout = iota
	// LayoutRowMajor puts translation at flat indices 3,7,11.
	// Pre-0.5 clients, and anything sending rowMajorOrder: true.
	LayoutRowMajor
)

func (l Layout) String() string {
	if l == LayoutRowMajor {
		return "row-major(3,7,11)"
	}
	return "column-major(12,13,14)"
}

// translationIndices returns the flat array positions of tx, ty, tz.
func (l Layout) translationIndices() [3]int {
	if l == LayoutRowMajor {
		return [3]int{3, 7, 11}
	}
	return [3]int{12, 13, 14}
}

// Matrix4 is a 4x4 affine transform in the wire's flat representation,
// together with the layout needed to interpret it.
type Matrix4 struct {
	M      [16]float64
	Layout Layout
}

// NewMatrix4 builds a Matrix4 from a flat slice.
func NewMatrix4(v []float64, l Layout) (Matrix4, error) {
	var m Matrix4
	if len(v) != 16 {
		return m, fmt.Errorf("navlib: affine needs 16 elements, got %d", len(v))
	}
	copy(m.M[:], v)
	m.Layout = l
	return m, nil
}

// Identity returns the identity transform in the given layout.
func Identity(l Layout) Matrix4 {
	return Matrix4{
		M:      [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1},
		Layout: l,
	}
}

// Translation extracts the translation component.
func (m Matrix4) Translation() [3]float64 {
	i := m.Layout.translationIndices()
	return [3]float64{m.M[i[0]], m.M[i[1]], m.M[i[2]]}
}

// SetTranslation replaces the translation component in place.
func (m *Matrix4) SetTranslation(t [3]float64) {
	i := m.Layout.translationIndices()
	m.M[i[0]], m.M[i[1]], m.M[i[2]] = t[0], t[1], t[2]
}

// Slice returns the flat wire representation.
func (m Matrix4) Slice() []float64 { return m.M[:] }

// Box is an axis-aligned bounding box as sent by the navlib:
// [minX, minY, minZ, maxX, maxY, maxZ].
type Box [6]float64

// Center returns the box centre, the usual default rotation pivot.
func (b Box) Center() [3]float64 {
	return [3]float64{
		(b[0] + b[3]) / 2,
		(b[1] + b[4]) / 2,
		(b[2] + b[5]) / 2,
	}
}

// Empty reports whether the box has no extent, which is what an unset or
// unsupported view.extents looks like.
func (b Box) Empty() bool {
	return b[0] == b[3] && b[1] == b[4] && b[2] == b[5]
}

// Scaled expands or contracts the box about its centre. Used for orthographic
// zoom, where the camera cannot dolly.
func (b Box) Scaled(f float64) Box {
	c := b.Center()
	var out Box
	for i := 0; i < 3; i++ {
		out[i] = c[i] + (b[i]-c[i])*f
		out[i+3] = c[i] + (b[i+3]-c[i])*f
	}
	return out
}

// Diagonal returns the length of the box diagonal, used for speed scaling.
func (b Box) Diagonal() float64 {
	dx, dy, dz := b[3]-b[0], b[4]-b[1], b[5]-b[2]
	return sqrt(dx*dx + dy*dy + dz*dz)
}
