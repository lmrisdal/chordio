//go:build linux

package main

import "testing"

func TestParseSelection(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want []int
	}{
		{"1", 3, []int{1}},
		{"1,2", 3, []int{1, 2}},
		{"1 3", 3, []int{1, 3}},
		{" 2 , 3 ", 3, []int{2, 3}},
		{"3", 3, []int{3}},
		{"0", 3, nil},
		{"4", 3, nil},
		{"x", 3, nil},
		{"1,x", 3, nil},
	}
	for _, c := range cases {
		got, err := parseSelection(c.in, c.max)
		if c.want == nil {
			if err == nil {
				t.Errorf("parseSelection(%q, %d): want error, got %v", c.in, c.max, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSelection(%q, %d): unexpected error %v", c.in, c.max, err)
			continue
		}
		for _, idx := range c.want {
			if !got[idx] {
				t.Errorf("parseSelection(%q, %d): missing %d in %v", c.in, c.max, idx, got)
			}
		}
		if len(got) != len(c.want) {
			t.Errorf("parseSelection(%q, %d): got %d picks, want %d", c.in, c.max, len(got), len(c.want))
		}
	}
}
