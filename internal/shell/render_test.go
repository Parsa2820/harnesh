package shell

import "testing"

func TestRuneWidth(t *testing.T) {
	tests := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'é', 1},
		{'→', 1},
		{'\u0301', 0}, // combining acute accent
		{'\u200d', 0}, // zero width joiner
		{'\ufe0f', 0}, // variation selector-16
		{'\x07', 0},   // control character
		{'漢', 2},
		{'한', 2},
		{'Ａ', 2}, // fullwidth Latin
		{'☕', 2},
		{'🚀', 2},
		{'\U0001F600', 2},
	}
	for _, tc := range tests {
		if got := runeWidth(tc.r); got != tc.want {
			t.Errorf("runeWidth(%U) = %d, want %d", tc.r, got, tc.want)
		}
	}
}

func TestWideRangesAreSorted(t *testing.T) {
	// runeWidth binary-searches wideRanges.
	for i, r := range wideRanges {
		if r[0] > r[1] {
			t.Errorf("range %d is inverted: %U-%U", i, r[0], r[1])
		}
		if i > 0 && wideRanges[i-1][1] >= r[0] {
			t.Errorf("range %d (%U-%U) overlaps or precedes range %d", i, r[0], r[1], i-1)
		}
	}
}
