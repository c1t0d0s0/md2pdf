package bib

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const sample = `
@string{pcs = "PeerJ Computer Science"}
@article{citationjs2019,
  author = {Willighagen, Lars G. and M{\"u}ller, Jean-Pierre and {Some Org}},
  title = {Citation.js: a {BibTeX} tool},
  journal = pcs,
  volume = {5}, number = 2,
  pages = {e214--e220},
  year = {2019},
  doi = {10.7717/peerj-cs.214}
}
@misc{web, author = {{CommonMark contributors}}, title = {Spec}, year = 2024, url = {https://spec.commonmark.org/}}
`

func render(nodes []*html.Node) string {
	var b bytes.Buffer
	for _, n := range nodes {
		html.Render(&b, n)
	}
	return b.String()
}

func TestParseAndFormat(t *testing.T) {
	entries, err := Parse("refs.bib", sample, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "citationjs2019" {
		t.Fatalf("entries: %+v", entries)
	}
	got := render(Format(entries[0]))
	want := `Willighagen LG, Müller JP, Some Org. Citation.js: a BibTeX tool. PeerJ Computer Science. 2019;5(2):e214–e220. doi:<a href="https://doi.org/10.7717/peerj-cs.214">10.7717/peerj-cs.214</a>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	got = render(Format(entries[1]))
	if !strings.HasPrefix(got, "CommonMark contributors. Spec [Internet]. 2024. Available from: <a") {
		t.Fatalf("got %s", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"@article{x, title = {unterminated", "@article{x, journal = undefinedmacro}", "@article{, title={t}}"} {
		if _, err := Parse("r.bib", src, map[string]string{}); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}

func TestNames(t *testing.T) {
	names, others := ParseNames("Ludwig van Beethoven and Doe, J. and others")
	if !others || len(names) != 2 || names[0].Family != "van Beethoven" || names[0].Given != "Ludwig" || names[1].Family != "Doe" {
		t.Fatalf("%+v %v", names, others)
	}
}
