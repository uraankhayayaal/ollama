package main

import (
	"reflect"
	"testing"
)

func TestSplitAllow(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"example.com", []string{"example.com"}},
		{"example.com, *.golang.org ,proxy.golang.org", []string{"example.com", "*.golang.org", "proxy.golang.org"}},
		{"", nil},
		{"  ,  ,", nil},
	}
	for _, c := range cases {
		if got := splitAllow(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitAllow(%q) = %v, ожидалось %v", c.in, got, c.want)
		}
	}
}
