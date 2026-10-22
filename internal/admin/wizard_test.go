package admin

import (
	"reflect"
	"testing"
)

func TestParseSelection(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []int
	}{
		{"1,3", 5, []int{0, 2}},
		{"all", 3, []int{0, 1, 2}},
		{"*", 2, []int{0, 1}},
		{"", 5, nil},
		{"2 4", 4, []int{1, 3}},
		{"9,1", 3, []int{0}},      // 9 out of range, ignored
		{"1,1,2", 3, []int{0, 1}}, // dedup
		{"x,2", 3, []int{1}},      // garbage ignored
	}
	for _, c := range cases {
		if got := parseSelection(c.in, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseSelection(%q,%d) = %v, want %v", c.in, c.n, got, c.want)
		}
	}
}
