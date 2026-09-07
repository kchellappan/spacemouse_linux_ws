package navlib

import "math"

// Mat4 is a 4x4 matrix in column-major storage: element (row, col) lives at
// m[col*4+row], and the translation column occupies indices 12, 13, 14.
//
// This is the canonical internal form. Wire data arrives in whichever layout
// the client uses; convert with Matrix4.Canonical and FromCanonical rather
// than doing arithmetic on wire values directly.
type Mat4 [16]float64

// At returns element (row, col).
func (m Mat4) At(row, col int) float64 { return m[col*4+row] }

// Set assigns element (row, col).
func (m *Mat4) Set(row, col int, v float64) { m[col*4+row] = v }

// Identity4 returns the identity matrix.
func Identity4() Mat4 {
	return Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
}

// Mul returns m*b, applied to column vectors right to left.
func (m Mat4) Mul(b Mat4) Mat4 {
	var out Mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			var s float64
			for k := 0; k < 4; k++ {
				s += m.At(row, k) * b.At(k, col)
			}
			out.Set(row, col, s)
		}
	}
	return out
}

// Transpose returns the transpose of m. Converting between the two wire
// layouts is exactly a transpose.
func (m Mat4) Transpose() Mat4 {
	var out Mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			out.Set(row, col, m.At(col, row))
		}
	}
	return out
}

// MulVec transforms a direction (w = 0): rotation and scale apply, translation
// does not. Use this for axes and offsets expressed in a local frame.
func (m Mat4) MulVec(v [3]float64) [3]float64 {
	var out [3]float64
	for row := 0; row < 3; row++ {
		out[row] = m.At(row, 0)*v[0] + m.At(row, 1)*v[1] + m.At(row, 2)*v[2]
	}
	return out
}

// MulPoint transforms a point (w = 1), discarding any perspective term.
func (m Mat4) MulPoint(p [3]float64) [3]float64 {
	var out [3]float64
	for row := 0; row < 3; row++ {
		out[row] = m.At(row, 0)*p[0] + m.At(row, 1)*p[1] + m.At(row, 2)*p[2] + m.At(row, 3)
	}
	return out
}

// Translation returns the translation column.
func (m Mat4) Translation() [3]float64 { return [3]float64{m[12], m[13], m[14]} }

// Translate returns a pure translation matrix.
func Translate(t [3]float64) Mat4 {
	m := Identity4()
	m[12], m[13], m[14] = t[0], t[1], t[2]
	return m
}

// RotateAxis returns a rotation of angle radians about the given axis, which
// need not be normalised. A zero-length axis yields the identity.
func RotateAxis(axis [3]float64, angle float64) Mat4 {
	l := math.Sqrt(axis[0]*axis[0] + axis[1]*axis[1] + axis[2]*axis[2])
	if l == 0 {
		return Identity4()
	}
	x, y, z := axis[0]/l, axis[1]/l, axis[2]/l
	c, s := math.Cos(angle), math.Sin(angle)
	t := 1 - c

	m := Identity4()
	m.Set(0, 0, t*x*x+c)
	m.Set(0, 1, t*x*y-s*z)
	m.Set(0, 2, t*x*z+s*y)
	m.Set(1, 0, t*x*y+s*z)
	m.Set(1, 1, t*y*y+c)
	m.Set(1, 2, t*y*z-s*x)
	m.Set(2, 0, t*x*z-s*y)
	m.Set(2, 1, t*y*z+s*x)
	m.Set(2, 2, t*z*z+c)
	return m
}

// OrbitAbout returns the transform that rotates about a world-space pivot:
// T(pivot) * R * T(-pivot).
func OrbitAbout(pivot [3]float64, r Mat4) Mat4 {
	neg := [3]float64{-pivot[0], -pivot[1], -pivot[2]}
	return Translate(pivot).Mul(r).Mul(Translate(neg))
}

// Canonical converts wire data to the internal column-major form.
func (m Matrix4) Canonical() Mat4 {
	c := Mat4(m.M)
	if m.Layout == LayoutRowMajor {
		return c.Transpose()
	}
	return c
}

// FromCanonical converts an internal matrix back to a client's wire layout.
func FromCanonical(c Mat4, l Layout) Matrix4 {
	if l == LayoutRowMajor {
		c = c.Transpose()
	}
	return Matrix4{M: [16]float64(c), Layout: l}
}
