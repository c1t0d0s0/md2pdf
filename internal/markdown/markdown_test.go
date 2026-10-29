package markdown

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/c1t0d0s0/md2pdf/internal/input"
)

func render(t *testing.T, src string) string {
	t.Helper()
	body, err := Build([]input.SourceFile{{Path: "/tmp/a.md", Name: "a.md", Content: src}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, s := range body.Sections {
		html.Render(&buf, s)
	}
	return buf.String()
}

func TestCJKEmphasis(t *testing.T) {
	out := render(t, "これは**「重要」**です")
	if !strings.Contains(out, "<strong>「重要」</strong>") {
		t.Fatalf("got %s", out)
	}
}

func TestMath(t *testing.T) {
	out := render(t, "a $x_1$ b $5 and $10 c\n\n$$\n\\int_0^1 f\n$$\n")
	if !strings.Contains(out, `<span class="pfpdf-math-src" data-display="0" data-line="1" data-file="a.md">x_1</span>`) {
		t.Fatalf("inline: %s", out)
	}
	if !strings.Contains(out, "$5 and $10") {
		t.Fatalf("dollars: %s", out)
	}
	if !strings.Contains(out, `data-display="1" data-line="3" data-file="a.md">\int_0^1 f</div>`) {
		t.Fatalf("block: %s", out)
	}
}

func TestHeadingsAndPageBreak(t *testing.T) {
	body, err := Build([]input.SourceFile{{Path: "/tmp/a.md", Name: "a.md", Content: "# Hello World\n\n___\n\n## Hello World\n\n---\n"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(body.TOC) != 2 || body.TOC[0].ID != "hello-world" || body.TOC[1].ID != "hello-world-2" {
		t.Fatalf("toc: %+v", body.TOC)
	}
	var buf bytes.Buffer
	html.Render(&buf, body.Sections[0])
	out := buf.String()
	if strings.Count(out, "data-pfpdf-page-break") != 1 || !strings.Contains(out, "<hr/>") {
		t.Fatalf("got %s", out)
	}
}

func TestHighlightAndWordBreak(t *testing.T) {
	out := render(t, "```go\nfunc main() {}\n```\n\nhttps://example.com/very/long/path/to/some/resource?query=1")
	if !strings.Contains(out, `class="language-go hljs"`) || !strings.Contains(out, `hljs-keyword`) {
		t.Fatalf("got %s", out)
	}
	if !strings.Contains(out, "<wbr/>") {
		t.Fatalf("no wbr: %s", out)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{"Hello, World!": "hello-world", "日本語 の 見出し": "日本語-の-見出し", "!!!": "section", "a_b-c": "a_b-c"}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
