package index

import (
	"reflect"
	"testing"
)

func TestParseLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "basic relative",
			in:   `<a href="foo.html">x</a>`,
			want: []string{"foo.html"},
		},
		{
			name: "missing extension",
			in:   `<a href="bar">x</a>`,
			want: []string{"bar.html"},
		},
		{
			name: "strip leading slash and ./",
			in:   `<a href="/a.html">a</a><a href="./b.html">b</a>`,
			want: []string{"a.html", "b.html"},
		},
		{
			name: "skip external and fragment",
			in:   `<a href="https://x.com">x</a><a href="#anchor">a</a><a href="mailto:x@y">m</a><a href="javascript:void(0)">j</a>`,
			want: nil,
		},
		{
			name: "dedupe and lowercase",
			in:   `<a href="Foo.html">1</a><a href="foo.html">2</a><a href="FOO.HTML">3</a>`,
			want: []string{"foo.html"},
		},
		{
			name: "sorted",
			in:   `<a href="z.html">z</a><a href="a.html">a</a><a href="m.html">m</a>`,
			want: []string{"a.html", "m.html", "z.html"},
		},
		{
			name: "strip query and trailing fragment",
			in:   `<a href="page.html?x=1#sec">p</a>`,
			want: []string{"page.html"},
		},
		{
			name: "nested anchor inside markup",
			in:   `<html><body><p>hi <a class="x" href="daily/2026-05-25.html">today</a></p></body></html>`,
			want: []string{"daily/2026-05-25.html"},
		},
		{
			name: "empty body",
			in:   ``,
			want: nil,
		},
		{
			name: "empty href",
			in:   `<a href="">x</a>`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLinks([]byte(tc.in))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
