package document

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslateCSS(t *testing.T) {
	css := `.cover-conf { string-set: pfpdf-confidential "● " content(); }
@page { @top-left { content: env(pub-title); } @bottom-left { content: string(pfpdf-confidential); }
  @top-right { content: string(pfpdf-toc-continuation, first-except); } }
.pfpdf-toc a::after { content: leader(".") target-counter(attr(href url), page); }
.run { position: running(conf); }`
	ctx := cssContext{title: `My "Doc"`, slotText: func(classes []string) (string, bool) {
		for _, c := range classes {
			if c == "cover-conf" {
				return "Confidential", true
			}
		}
		return "", false
	}}
	named := map[string]string{}
	collectNamedStrings([]string{css}, ctx, named)
	res := translateCSS(css, ctx, named)
	for _, want := range []string{
		`content: "My \"Doc\""`,
		`content: "● Confidential"`,
		`content: ""`,
		`content: attr(data-md2pdf-page)`,
		`display: none`,
	} {
		if !strings.Contains(res.css, want) {
			t.Errorf("missing %q in\n%s", want, res.css)
		}
	}
	if !res.leader || !res.usesPages {
		t.Errorf("flags: %+v", res)
	}
}

func TestLoadCSSImportsAndURLs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "t")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(dir, "common.css"), []byte("/* c */ .a { color: red; }"), 0o644)
	os.WriteFile(filepath.Join(sub, "bg.svg"), []byte("<svg/>"), 0o644)
	os.WriteFile(filepath.Join(sub, "style.css"), []byte(`@import "../common.css";
.b { background: url("./bg.svg"), url(data:image/png;base64,AA==); }`), 0o644)
	css, err := loadCSS(filepath.Join(sub, "style.css"), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(css, ".a { color: red; }") || !strings.Contains(css, `url("file://`+filepath.ToSlash(sub)+`/bg.svg")`) ||
		!strings.Contains(css, "url(data:image/png;base64,AA==)") || strings.Contains(css, "/* c */") {
		t.Fatalf("got %s", css)
	}
	os.WriteFile(filepath.Join(sub, "bad.css"), []byte(`.x { background: url(missing.png); }`), 0o644)
	if _, err := loadCSS(filepath.Join(sub, "bad.css"), map[string]bool{}); err == nil {
		t.Fatal("expected missing resource error")
	}
}
