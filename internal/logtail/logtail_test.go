package logtail

import (
	"reflect"
	"testing"
)

func TestNew(t *testing.T) {
	cases := []struct {
		name       string
		prev, next []string
		fresh      []string
		gap        bool
	}{
		{"first read", nil, []string{"a", "b"}, []string{"a", "b"}, false},
		{"nothing new", []string{"a", "b"}, []string{"a", "b"}, []string{}, false},
		{"two new", []string{"a", "b", "c"}, []string{"b", "c", "d", "e"}, []string{"d", "e"}, false},
		{"window slid", []string{"a", "b", "c"}, []string{"c", "d", "e"}, []string{"d", "e"}, false},
		{"repeated lines print once", []string{"x", "x", "x"}, []string{"x", "x", "x", "x"}, []string{"x"}, false},
		{"no overlap", []string{"a", "b"}, []string{"y", "z"}, []string{"y", "z"}, true},
		{"empty read", []string{"a"}, nil, nil, false},
	}
	for _, c := range cases {
		fresh, gap := New(c.prev, c.next)
		if len(fresh) == 0 && len(c.fresh) == 0 {
			fresh, c.fresh = nil, nil
		}
		if !reflect.DeepEqual(fresh, c.fresh) || gap != c.gap {
			t.Errorf("%s: got %v gap=%v, want %v gap=%v", c.name, fresh, gap, c.fresh, c.gap)
		}
	}
}
