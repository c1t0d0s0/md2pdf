package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"docs/", "--no-toc", "-t", "book", "--font-dir", "a", "--font-dir=b", "--timeout", "90s"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Input != "docs/" || o.Output != "docs.pdf" || *o.TOC || *o.Template != "book" || len(o.FontDirs) != 2 || o.Timeout.Seconds() != 90 {
		t.Fatalf("%+v", o)
	}
	o, err = parseArgs([]string{"--input", "a.md"})
	if err != nil || o.Output != "a.pdf" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown option":       {"--bogus"},
		"may not be combined":  {"a.md", "--toc", "--no-toc"},
		"may not be repeated":  {"a.md", "-o", "x.pdf", "--output", "y.pdf"},
		"missing value":        {"a.md", "--title"},
		"required":             {},
		"not a bundled preset": {"a.md", "--template", "/nonexistent"},
		"command modes":        {"--help", "--version"},
		"--logo and --no-logo": {"a.md", "--logo", "x.png", "--no-logo"},
		"expected a duration":  {"a.md", "--timeout", "soon"},
		"HTML other than <br>": {"a.md", "--title", "<i>x</i>"},
	}
	for want, args := range cases {
		if _, err := parseArgs(args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want %q", args, err, want)
		}
	}
}
