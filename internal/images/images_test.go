package images

import "testing"

// Owner of the classification boundaries. Both tests are needed: a
// wordmark is wide but short, an avatar square but tiny; either test alone
// misses one of them.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		want Kind
	}{
		{"tracking beacon 1x1", 1, 1, Pixel},
		{"spacer 2x500 is a pixel by edge", 2, 500, Pixel},
		{"wordmark 319x43: min edge catches it", 319, 43, Small},
		{"avatar 108x108: area catches it", 108, 108, Small},
		{"just under the edge 99x400", 99, 400, Small},
		{"at the edge and area 200x200", 200, 200, Image},
		{"screenshot 640x480", 640, 480, Image},
		{"unknown dimensions are not withheld", 0, 0, Image},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.w, tc.h); got != tc.want {
				t.Errorf("Classify(%d,%d) = %s, want %s", tc.w, tc.h, got, tc.want)
			}
		})
	}
}
