package input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts() Options {
	return Options{IsTemplate: func(n string) bool { return n == "default" }, Now: time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)}
}

func TestResolveFrontMatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "02_b.md", "# B\n")
	write(t, dir, "01_a.md", "---\r\ntitle: A<br>B &amp; C\r\nauthor: Me\r\npage_size: A5\r\nlang: en\r\nconfidential: true\r\ntoc: false\r\n---\r\n# A\r\n")
	doc, err := Resolve(dir, opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Files) != 2 || doc.Files[0].Name != "01_a.md" || doc.Files[0].LineOffset != 8 {
		t.Fatalf("files: %+v", doc.Files)
	}
	if doc.Files[0].Content != "# A\r\n" {
		t.Fatalf("body: %q", doc.Files[0].Content)
	}
	md := doc.Metadata
	if strings.Join(md.Title.Lines, "|") != "A|B & C" || md.Author != "Me" || md.PageSize != "148mm 210mm" ||
		md.Lang != "en" || !md.Confidential || md.Date != "2025-03-04" {
		t.Fatalf("metadata: %+v", md)
	}
	if doc.Settings.TOC == nil || *doc.Settings.TOC {
		t.Fatalf("toc setting: %+v", doc.Settings)
	}
}

func TestJapaneseDate(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.md", "---\ntitle: T\n---\n")
	epoch := int64(1767225600)
	o := opts()
	o.SourceEpoch = &epoch
	doc, err := Resolve(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Date != "2026 年 1 月 1 日" {
		t.Fatalf("date: %q", doc.Metadata.Date)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]string{
		"---\ntitle: T\nunknown: 1\n---\n":        "unknown front matter key",
		"# no front matter\n":                     "title is required",
		"---\ntitle: <b>x</b>\n---\n":             "HTML other than <br>",
		"---\ntitle: T\npage_size: huge\n---\n":   "page_size",
		"---\ntitle: T\ntemplate: nope\n---\n":    "unknown bundled template",
		"---\ntitle: T\n":                         "not closed",
		"---\ntitle: T\nconfidential: yes\n---\n": "expected a YAML boolean",
	}
	for content, want := range cases {
		dir := t.TempDir()
		p := write(t, dir, "a.md", content)
		_, err := Resolve(p, opts())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", content, err, want)
		}
	}
}

func TestParsePageSize(t *testing.T) {
	for in, want := range map[string]string{"A4": "210mm 297mm", "letter": "8.5in 11in", "100mm 200mm": "100mm 200mm"} {
		got, err := ParsePageSize(in)
		if err != nil || got != want {
			t.Errorf("ParsePageSize(%q) = %q, %v", in, got, err)
		}
	}
}
