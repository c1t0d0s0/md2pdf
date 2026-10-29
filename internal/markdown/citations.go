package markdown

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/c1t0d0s0/md2pdf/internal/bib"
	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/input"
)

// cluster is a single occurrence of \cite{...}.
type cluster struct {
	id   string
	keys []string
	file string
	line int
}

// citeState collects citation syntax found while parsing one file.
type citeState struct {
	file     input.SourceFile
	clusters []*cluster
	markers  int
	err      error
}

func (s *citeState) fail(line int, format string, args ...any) {
	if s.err == nil {
		s.err = errs.Input("%s:%d: %s", s.file.Name, line+s.file.LineOffset, fmt.Sprintf(format, args...))
	}
}

// KindCitation is the AST kind for the \cite{...} command.
var KindCitation = ast.NewNodeKind("Citation")

// Citation is a placeholder resolved after bibliography numbering.
type Citation struct {
	ast.BaseInline
	cluster *cluster
}

func (n *Citation) Kind() ast.NodeKind { return KindCitation }
func (n *Citation) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"ID": n.cluster.id}, nil)
}

// KindBibliography is the AST kind for a \printbibliography paragraph.
var KindBibliography = ast.NewNodeKind("Bibliography")

// Bibliography marks where the reference list is placed.
type Bibliography struct{ ast.BaseBlock }

func (n *Bibliography) Kind() ast.NodeKind { return KindBibliography }
func (n *Bibliography) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

var citeKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_:.#$%&+?<>~/-]*$`)

type citationParser struct{ st *citeState }

func (p *citationParser) Trigger() []byte { return []byte{'\\'} }

func (p *citationParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	const cmd = `\cite{`
	if !bytes.HasPrefix(line, []byte(cmd)) {
		return nil
	}
	lineNo := lineOf(block.Source(), seg.Start)
	end := bytes.IndexByte(line, '}')
	if end < 0 {
		p.st.fail(lineNo, `unclosed \cite command`)
		return nil
	}
	body := string(line[len(cmd):end])
	if strings.Contains(body, "{") {
		p.st.fail(lineNo, `nested braces in \cite are not supported`)
		return nil
	}
	keys := strings.Split(body, ",")
	seen := map[string]bool{}
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if !citeKeyRe.MatchString(k) {
			p.st.fail(lineNo, `invalid or empty bibliography key in \cite`)
			return nil
		}
		if seen[k] {
			p.st.fail(lineNo, `duplicate bibliography key in one \cite`)
			return nil
		}
		seen[k] = true
		keys[i] = k
	}
	block.Advance(end + 1)
	c := &cluster{
		id:   fmt.Sprintf("pfpdf-citation-%s-%d", base64.RawURLEncoding.EncodeToString([]byte(p.st.file.Name)), len(p.st.clusters)+1),
		keys: keys,
		file: p.st.file.Name,
		line: lineNo + p.st.file.LineOffset,
	}
	p.st.clusters = append(p.st.clusters, c)
	return &Citation{cluster: c}
}

type citationTransformer struct{ st *citeState }

func (t *citationTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	for c := doc.FirstChild(); c != nil; {
		next := c.NextSibling()
		if c.Kind() == ast.KindParagraph {
			var buf bytes.Buffer
			for i := 0; i < c.Lines().Len(); i++ {
				seg := c.Lines().At(i)
				buf.Write(seg.Value(source))
			}
			if strings.TrimSpace(buf.String()) == `\printbibliography` {
				doc.ReplaceChild(doc, c, &Bibliography{})
				t.st.markers++
			}
		}
		c = next
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		cite, ok := n.(*Citation)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		for p := n.Parent(); p != nil; p = p.Parent() {
			switch p.Kind() {
			case ast.KindHeading:
				t.st.fail(cite.cluster.line-t.st.file.LineOffset, "citations are not allowed in headings")
			case ast.KindLink:
				t.st.fail(cite.cluster.line-t.st.file.LineOffset, "citations are not allowed in links")
			}
		}
		return ast.WalkContinue, nil
	})
}

type citationRenderer struct{}

func (r *citationRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindCitation, func(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			fmt.Fprintf(w, `<span data-pfpdf-citation="%s"></span>`, n.(*Citation).cluster.id)
		}
		return ast.WalkSkipChildren, nil
	})
	reg.Register(KindBibliography, func(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			w.WriteString(`<div data-pfpdf-bibliography-marker=""></div>` + "\n")
		}
		return ast.WalkSkipChildren, nil
	})
}

// prepareBibliography parses the database and numbers cited entries in
// order of first citation.
func prepareBibliography(files []input.BibliographyFile, clusters []*cluster) ([]bib.Entry, error) {
	if len(clusters) == 0 && len(files) == 0 {
		return nil, nil
	}
	if len(files) == 0 {
		c := clusters[0]
		return nil, errs.Input(`%s:%d: \cite requires bibliography front matter`, c.file, c.line)
	}
	macros := map[string]string{}
	byKey := map[string]bib.Entry{}
	owner := map[string]string{}
	for _, f := range files {
		entries, err := bib.Parse(f.Path, f.Content, macros)
		if err != nil {
			return nil, errs.Input("%v", err)
		}
		for _, e := range entries {
			if prev, ok := owner[e.Key]; ok {
				return nil, errs.Input("duplicate bibliography key %q in %s and %s", e.Key, prev, f.Path)
			}
			owner[e.Key] = f.Path
			byKey[e.Key] = e
		}
	}
	var ordered []bib.Entry
	seen := map[string]bool{}
	for _, c := range clusters {
		for _, k := range c.keys {
			e, ok := byKey[k]
			if !ok {
				return nil, errs.Input("%s:%d: bibliography key not found: %q", c.file, c.line, k)
			}
			if !seen[k] {
				seen[k] = true
				ordered = append(ordered, e)
			}
		}
	}
	return ordered, nil
}

type backlink struct{ id, label string }

// renderCitations replaces citation placeholders with numbered links and
// places the reference list at the marker or at the end of the document.
func renderCitations(roots []*html.Node, clusters []*cluster, entries []bib.Entry, ids *idAllocator) error {
	refsID := ids.allocate("refs")
	number := map[string]int{}
	entryID := map[string]string{}
	for i, e := range entries {
		number[e.Key] = i + 1
		entryID[e.Key] = ids.allocate("pfpdf-bib-" + base64.RawURLEncoding.EncodeToString([]byte(e.Key)))
	}
	byID := map[string]*cluster{}
	for _, c := range clusters {
		byID[c.id] = c
	}
	backlinks := map[string][]backlink{}
	var marker *html.Node
	for _, root := range roots {
		var placeholders []*html.Node
		eachElement([]*html.Node{root}, func(n *html.Node) {
			if _, ok := lookupAttr(n, "data-pfpdf-citation"); ok {
				placeholders = append(placeholders, n)
			}
			if _, ok := lookupAttr(n, "data-pfpdf-bibliography-marker"); ok && marker == nil {
				marker = n
			}
		})
		for _, ph := range placeholders {
			c := byID[getAttr(ph, "data-pfpdf-citation")]
			if c == nil {
				return errs.Runtime("internal citation cluster not found")
			}
			keys := append([]string(nil), c.keys...)
			sortByNumber(keys, number)
			span := elemWith("span", "class", "pfpdf-citation")
			span.AppendChild(textNode("["))
			for i, k := range keys {
				if i > 0 {
					span.AppendChild(textNode(", "))
				}
				anchor := ids.allocate(fmt.Sprintf("%s-%d", c.id, i+1))
				a := elemWith("a", "id", anchor, "href", "#"+entryID[k], "class", "pfpdf-citation-link", "role", "doc-biblioref")
				a.AppendChild(textNode(strconv.Itoa(number[k])))
				span.AppendChild(a)
				backlinks[k] = append(backlinks[k], backlink{anchor, fmt.Sprintf("%s:%d", c.file, c.line)})
			}
			span.AppendChild(textNode("]"))
			ph.Parent.InsertBefore(span, ph)
			ph.Parent.RemoveChild(ph)
		}
	}

	container := elemWith("div", "id", refsID, "class", "pfpdf-bibliography csl-bib-body", "role", "doc-bibliography")
	for i, e := range entries {
		entry := elemWith("div", "class", "csl-entry pfpdf-bibliography-entry", "id", entryID[e.Key])
		left := elemWith("div", "class", "csl-left-margin")
		left.AppendChild(textNode(strconv.Itoa(i+1) + ". "))
		right := elemWith("div", "class", "csl-right-inline")
		for _, n := range bib.Format(e) {
			right.AppendChild(n)
		}
		entry.AppendChild(left)
		entry.AppendChild(right)
		if bl := backlinks[e.Key]; len(bl) > 0 {
			box := elemWith("span", "class", "pfpdf-bibliography-backlinks")
			box.AppendChild(textNode(" "))
			for j, b := range bl {
				if j > 0 {
					box.AppendChild(textNode(" "))
				}
				a := elemWith("a", "href", "#"+b.id, "class", "pfpdf-bibliography-backlink",
					"aria-label", "Back to citation "+b.label, "role", "doc-backlink")
				a.AppendChild(textNode("↩"))
				box.AppendChild(a)
			}
			entry.AppendChild(box)
		}
		container.AppendChild(entry)
	}
	if marker != nil {
		marker.Parent.InsertBefore(container, marker)
		marker.Parent.RemoveChild(marker)
	} else {
		roots[len(roots)-1].AppendChild(container)
	}
	return nil
}

func sortByNumber(keys []string, number map[string]int) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && number[keys[j]] < number[keys[j-1]]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

func textNode(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }

func elemWith(tag string, kv ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
	for i := 0; i+1 < len(kv); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
	}
	return n
}
