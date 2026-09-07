package navlib

import "testing"

func TestQuirksFor(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		info  ClientInfo
		want  Layout
		frame bool
	}{
		{"modern default", ClientInfo{Version: 0.8, RowMajorOrder: &no}, LayoutColumnMajor, true},
		{"modern explicit row-major", ClientInfo{Version: 0.8, RowMajorOrder: &yes}, LayoutRowMajor, true},
		{"pre-0.5 online sample", ClientInfo{Version: 0, Name: "web_threejs.html"}, LayoutRowMajor, false},
		{"0.5 boundary, no flag", ClientInfo{Version: 0.5}, LayoutColumnMajor, false},
		{"0.6 gains frame timing", ClientInfo{Version: 0.6}, LayoutColumnMajor, true},
		{"unknown client, no flag", ClientInfo{Version: 0.8}, LayoutColumnMajor, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := QuirksFor(tc.info)
			if q.Layout != tc.want {
				t.Errorf("Layout = %v, want %v", q.Layout, tc.want)
			}
			if q.FrameTiming != tc.frame {
				t.Errorf("FrameTiming = %v, want %v", q.FrameTiming, tc.frame)
			}
		})
	}
}

func TestMatrixTranslationByLayout(t *testing.T) {
	// Same logical transform (translate 1,2,3) in each wire layout.
	col := []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 1, 2, 3, 1}
	row := []float64{1, 0, 0, 1, 0, 1, 0, 2, 0, 0, 1, 3, 0, 0, 0, 1}

	m, err := NewMatrix4(col, LayoutColumnMajor)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Translation(); got != [3]float64{1, 2, 3} {
		t.Errorf("column-major translation = %v, want [1 2 3]", got)
	}

	m, err = NewMatrix4(row, LayoutRowMajor)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Translation(); got != [3]float64{1, 2, 3} {
		t.Errorf("row-major translation = %v, want [1 2 3]", got)
	}

	// Misreading the layout must not silently produce [1 2 3].
	m, _ = NewMatrix4(row, LayoutColumnMajor)
	if got := m.Translation(); got == [3]float64{1, 2, 3} {
		t.Error("row-major data read as column-major should not yield the right translation")
	}
}

func TestMatrixSetTranslationRoundTrips(t *testing.T) {
	for _, l := range []Layout{LayoutColumnMajor, LayoutRowMajor} {
		m := Identity(l)
		m.SetTranslation([3]float64{4, 5, 6})
		if got := m.Translation(); got != [3]float64{4, 5, 6} {
			t.Errorf("%v: round trip = %v, want [4 5 6]", l, got)
		}
	}
}

func TestNewMatrix4RejectsWrongLength(t *testing.T) {
	if _, err := NewMatrix4([]float64{1, 2, 3}, LayoutColumnMajor); err == nil {
		t.Error("expected an error for a 3-element affine")
	}
}

func TestBoxGeometry(t *testing.T) {
	b := Box{-3, -0.75, 2.5, 0, 0.75, 5.5}
	if got := b.Center(); got != [3]float64{-1.5, 0, 4} {
		t.Errorf("Center = %v, want [-1.5 0 4]", got)
	}
	// dx=3, dy=1.5, dz=3 -> sqrt(20.25) = 4.5
	if d := b.Diagonal(); d != 4.5 {
		t.Errorf("Diagonal = %v, want 4.5", d)
	}
}
