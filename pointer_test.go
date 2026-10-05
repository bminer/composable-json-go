package composablejson_test

import (
	"errors"
	"slices"
	"testing"

	composablejson "github.com/bminer/composable-json-go"
)

func TestParsePointer(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want composablejson.Pointer
	}{
		{"", composablejson.Pointer{}},
		{"/", composablejson.Pointer{""}},
		{"/a/0", composablejson.Pointer{"a", "0"}},
		{"/a~1b/m~0n", composablejson.Pointer{"a/b", "m~n"}},
		{"/~01", composablejson.Pointer{"~1"}},
	} {
		got, err := composablejson.ParsePointer(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ParsePointer(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
		if s := got.String(); s != tc.in {
			t.Errorf("%q.String() = %q, want %q", got, s, tc.in)
		}
	}
	for _, in := range []string{"a", "/~", "/~2"} {
		if _, err := composablejson.ParsePointer(in); !errors.Is(err, composablejson.ErrMalformedDirective) {
			t.Errorf("ParsePointer(%q): want ErrMalformedDirective, got %v", in, err)
		}
	}
}

func TestParseFragment(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want composablejson.Fragment
	}{
		{"", composablejson.Fragment{Pointer: composablejson.Pointer{}}},
		{"#", composablejson.Fragment{Pointer: composablejson.Pointer{}}},
		{"#/a/0", composablejson.Fragment{Pointer: composablejson.Pointer{"a", "0"}}},
		{"#iA/sine/rms", composablejson.Fragment{Anchor: "iA", Pointer: composablejson.Pointer{"sine", "rms"}}},
		{"iA", composablejson.Fragment{Anchor: "iA"}},
		{"_svc.v1-blue/port", composablejson.Fragment{Anchor: "_svc.v1-blue", Pointer: composablejson.Pointer{"port"}}},
		{"#/c%20d/e%23f", composablejson.Fragment{Pointer: composablejson.Pointer{"c d", "e#f"}}},
	} {
		got, err := composablejson.ParseFragment(tc.in)
		if err != nil || got.Anchor != tc.want.Anchor || !slices.Equal(got.Pointer, tc.want.Pointer) {
			t.Errorf("ParseFragment(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
		again, err := composablejson.ParseFragment(got.String())
		if err != nil || again.Anchor != got.Anchor || !slices.Equal(again.Pointer, got.Pointer) {
			t.Errorf("ParseFragment(%q.String() = %q) = %+v, %v", tc.in, got.String(), again, err)
		}
	}
	for _, in := range []string{"1st", "a b", "#%zz"} {
		if _, err := composablejson.ParseFragment(in); !errors.Is(err, composablejson.ErrMalformedDirective) {
			t.Errorf("ParseFragment(%q): want ErrMalformedDirective, got %v", in, err)
		}
	}
}
