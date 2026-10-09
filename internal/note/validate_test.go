package note

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{name: "simple", in: "a.md", want: "a.md"},
		{name: "nested", in: "eng/runbooks/deploy.md", want: "eng/runbooks/deploy.md"},
		{name: "trims whitespace", in: "  a/b.md \t\n", want: "a/b.md"},
		{name: "trims slashes", in: "/a/b.md/", want: "a/b.md"},
		{name: "trims many slashes", in: "///a///", want: "a"},
		{name: "slashes then whitespace", in: " / a.md / ", want: "a.md"},
		{name: "collapses repeated slashes", in: "a//b///c.md", want: "a/b/c.md"},
		{name: "case preserved", in: "Eng/ReadMe.MD", want: "Eng/ReadMe.MD"},
		{name: "inner spaces kept", in: "my notes/to do.md", want: "my notes/to do.md"},
		{name: "dots inside names ok", in: "a/.hidden/..x/y..md", want: "a/.hidden/..x/y..md"},
		{name: "unicode ok", in: "日本語/ノート.md", want: "日本語/ノート.md"},
		{name: "backslash is an ordinary char", in: `a\b.md`, want: `a\b.md`},
		{name: "exactly 512 bytes", in: strings.Repeat(strings.Repeat("a", 127)+"/", 3) + strings.Repeat("a", 128), want: strings.Repeat(strings.Repeat("a", 127)+"/", 3) + strings.Repeat("a", 128)},
		{name: "segment of 255 bytes", in: strings.Repeat("a", 255), want: strings.Repeat("a", 255)},

		{name: "empty", in: "", bad: true},
		{name: "whitespace only", in: " \t ", bad: true},
		{name: "slashes only", in: "///", bad: true},
		{name: "dot", in: ".", bad: true},
		{name: "dotdot", in: "..", bad: true},
		{name: "dot segment", in: "a/./b", bad: true},
		{name: "dotdot segment", in: "a/../b", bad: true},
		{name: "leading dotdot", in: "../a", bad: true},
		{name: "trailing dotdot", in: "a/..", bad: true},
		{name: "newline inside", in: "a\nb", bad: true},
		{name: "tab inside", in: "a\tb", bad: true},
		{name: "nul", in: "a\x00b", bad: true},
		{name: "del", in: "a\x7fb", bad: true},
		{name: "c1 control", in: "a\u0085b", bad: true},
		{name: "invalid utf8", in: "a\xffb", bad: true},
		{name: "segment of 256 bytes", in: strings.Repeat("a", 256), bad: true},
		{name: "multibyte segment over 255 bytes", in: strings.Repeat("日", 86), bad: true},
		{name: "513 bytes total", in: strings.Repeat(strings.Repeat("a", 127)+"/", 3) + strings.Repeat("a", 129), bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePath(tt.in)
			if tt.bad {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("NormalizePath(%q) = %q, %v; want ErrInvalid", tt.in, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("NormalizePath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
			// Idempotent: normalizing a canonical path changes nothing.
			if again, err := NormalizePath(got); err != nil || again != got {
				t.Fatalf("not idempotent: %q -> %q, %v", got, again, err)
			}
		})
	}
}

func TestNormalizeFolderPrefix(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{in: "", want: ""},
		{in: "  ", want: ""},
		{in: "/", want: ""},
		{in: "eng", want: "eng/"},
		{in: "eng/", want: "eng/"},
		{in: "/eng//runbooks/", want: "eng/runbooks/"},
		{in: "a/../b", bad: true},
		{in: "a\x00", bad: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			got, err := NormalizeFolderPrefix(tt.in)
			if tt.bad {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("got %q, %v; want ErrInvalid", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestNormalizeTags(t *testing.T) {
	many := make([]string, 33)
	for i := range many {
		many[i] = fmt.Sprintf("t%02d", i)
	}
	tests := []struct {
		name string
		in   []string
		want []string
		bad  bool
	}{
		{name: "nil becomes empty", in: nil, want: []string{}},
		{name: "trim lower dedupe sort", in: []string{" Go ", "go", "API", "api "}, want: []string{"api", "go"}},
		{name: "blank dropped", in: []string{"", "  ", "x"}, want: []string{"x"}},
		{name: "exactly 32", in: many[:32], want: many[:32]},
		{name: "33 rejected", in: many, bad: true},
		{name: "duplicates do not count toward max", in: append(slices.Clone(many[:32]), "T00"), want: many[:32]},
		{name: "64 chars ok", in: []string{strings.Repeat("a", 64)}, want: []string{strings.Repeat("a", 64)}},
		{name: "65 chars rejected", in: []string{strings.Repeat("a", 65)}, bad: true},
		{name: "64 multibyte chars ok", in: []string{strings.Repeat("日", 64)}, want: []string{strings.Repeat("日", 64)}},
		{name: "control char rejected", in: []string{"a\tb"}, bad: true},
		{name: "invalid utf8 rejected", in: []string{"a\xff"}, bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeTags(tt.in)
			if tt.bad {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("got %v, %v; want ErrInvalid", got, err)
				}
				return
			}
			if err != nil || got == nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	if got := (Note{Path: "eng/runbooks/deploy.md"}).Title(); got != "deploy.md" {
		t.Fatalf("Title = %q", got)
	}
	if got := (Note{Path: "root.md"}).Title(); got != "root.md" {
		t.Fatalf("Title = %q", got)
	}
}
