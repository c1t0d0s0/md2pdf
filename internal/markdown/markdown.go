// Package markdown converts Markdown sources into HTML DOM sections with
// heading IDs, a table of contents, highlighted code, and resolved local resources.
package markdown

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"

	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/input"
)

// TOCEntry is a single heading listed in the table of contents.
type TOCEntry struct {
	Depth int
	ID    string
	Text  string
}

// Body is the converted document content.
type Body struct {
	Sections     []*html.Node // one <section> per source file
	TOC          []TOCEntry
	NeedsMath    bool
	NeedsMermaid bool
}

// Options controls conversion.
type Options struct {
	ReservedIDs  []string
	Bibliography []input.BibliographyFile
	Warn         func(string)
}

// FileAnchor is the ID of the section generated for the i-th source file.
func FileAnchor(i int) string { return fmt.Sprintf("pfpdf-file-%04d", i+1) }

type parsed struct {
	file  input.SourceFile
	nodes []*html.Node // top-level fragment nodes
}

// Build converts every source file and assembles the document body.
func Build(files []input.SourceFile, opts Options) (*Body, error) {
	warn := opts.Warn
	if warn == nil {
		warn = func(string) {}
	}
	var parses []*parsed
	var clusters []*cluster
	markers := 0
	for i, f := range files {
		st := &citeState{file: f}
		md := newMarkdown(fmt.Sprintf("f%d-", i+1), st)
		var buf bytes.Buffer
		if err := md.Convert([]byte(f.Content), &buf); err != nil {
			return nil, errs.Input("%s: cannot parse Markdown: %v", f.Name, err)
		}
		if st.err != nil {
			return nil, st.err
		}
		clusters = append(clusters, st.clusters...)
		markers += st.markers
		nodes, err := html.ParseFragment(&buf, &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
		if err != nil {
			return nil, errs.Runtime("%s: cannot parse generated HTML: %v", f.Name, err)
		}
		parses = append(parses, &parsed{file: f, nodes: nodes})
	}

	ids := newIDAllocator()
	for _, id := range opts.ReservedIDs {
		ids.reserve(id)
	}
	for i := range parses {
		ids.reserve(FileAnchor(i))
	}
	for _, p := range parses {
		eachElement(p.nodes, func(n *html.Node) {
			if _, marked := lookupAttr(n, headingMarker); marked {
				return
			}
			if id, ok := lookupAttr(n, "id"); ok {
				ids.reserve(id)
			}
		})
	}

	if markers > 1 {
		return nil, errs.Input(`\printbibliography may appear only once`)
	}
	if markers > 0 && len(clusters) == 0 {
		return nil, errs.Input(`\printbibliography requires at least one citation`)
	}
	entries, err := prepareBibliography(opts.Bibliography, clusters)
	if err != nil {
		return nil, err
	}
	roots := make([]*html.Node, len(parses))
	for i, p := range parses {
		roots[i] = wrap(p.nodes)
	}
	if len(clusters) > 0 {
		if err := renderCitations(roots, clusters, entries, ids); err != nil {
			return nil, err
		}
	}

	body := &Body{}
	for _, root := range roots {
		eachElement([]*html.Node{root}, func(n *html.Node) {
			if _, marked := lookupAttr(n, headingMarker); !marked {
				return
			}
			removeAttr(n, headingMarker)
			depth := int(n.Data[1] - '0')
			text := elementText(n)
			id, ok := lookupAttr(n, "id")
			if !ok {
				id = ids.allocate(Slugify(text))
				setAttr(n, "id", id)
			}
			body.TOC = append(body.TOC, TOCEntry{Depth: depth, ID: id, Text: text})
		})
	}

	fileIndex := map[string]int{}
	for i, p := range parses {
		fileIndex[p.file.Name] = i
	}
	for i, p := range parses {
		root := roots[i]
		if err := rewriteResources(root, p.file, fileIndex); err != nil {
			return nil, err
		}
		if convertMermaid(root, p.file) {
			body.NeedsMermaid = true
		}
		decorateTables(root)
		highlightCode(root, p.file, warn)
		if hasClass(root, "pfpdf-math-src") {
			body.NeedsMath = true
			annotateMath(root, p.file)
		}
		insertWordBreaks(root)
		stripLineAttrs(root)

		section := &html.Node{Type: html.ElementNode, Data: "section", DataAtom: atom.Section,
			Attr: []html.Attribute{{Key: "id", Val: FileAnchor(i)}, {Key: "class", Val: "pfpdf-source-file"}}}
		collapsePageBreaks(root, section)
		body.Sections = append(body.Sections, section)
	}
	return body, nil
}

func wrap(nodes []*html.Node) *html.Node {
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	return root
}

// ---------------------------------------------------------------------------
// IDs and slugs

type idAllocator struct {
	used   map[string]bool
	cursor map[string]int
}

func newIDAllocator() *idAllocator {
	return &idAllocator{used: map[string]bool{}, cursor: map[string]int{}}
}

func (a *idAllocator) reserve(id string) { a.used[id] = true }

func (a *idAllocator) allocate(base string) string {
	if !a.used[base] {
		a.used[base] = true
		return base
	}
	n := a.cursor[base]
	if n == 0 {
		n = 2
	}
	for a.used[fmt.Sprintf("%s-%d", base, n)] {
		n++
	}
	a.cursor[base] = n + 1
	id := fmt.Sprintf("%s-%d", base, n)
	a.used[id] = true
	return id
}

var spaceRunRe = regexp.MustCompile(`\s+`)

// Slugify derives a heading ID: it applies NFC normalization and lowercasing,
// strips ASCII punctuation except '_' and '-', and replaces whitespace runs with '-'.
func Slugify(text string) string {
	s := strings.ToLower(norm.NFC.String(text))
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		if r < 0x80 && unicode.IsPunct(r) || r < 0x80 && unicode.IsSymbol(r) {
			if r != '_' && r != '-' {
				continue
			}
		}
		b.WriteRune(r)
	}
	s = strings.Trim(spaceRunRe.ReplaceAllString(b.String(), "-"), "-")
	if s == "" {
		return "section"
	}
	return s
}

// ---------------------------------------------------------------------------
// Links and resources

var schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

func hasSchemeOrNetworkPath(u string) bool {
	return strings.HasPrefix(u, "//") || schemeRe.MatchString(u)
}

// resourceAttrs lists, per element, the attributes that fetch resources.
var resourceAttrs = map[string][]string{
	"img":    {"src"},
	"source": {"src"},
	"video":  {"src", "poster"},
	"audio":  {"src"},
	"track":  {"src"},
	"iframe": {"src"},
	"embed":  {"src"},
	"script": {"src"},
	"input":  {"src"},
	"object": {"data"},
	"link":   {"href"},
	"image":  {"href", "xlink:href"},
	"use":    {"href", "xlink:href"},
}

// FileURL converts an absolute path to a file:// URL.
func FileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}

// ResolveLocal resolves a relative URL against baseDir into a file:// URL
// and checks that the file exists.
func ResolveLocal(raw, baseDir, context string) (string, error) {
	clean, suffix := raw, ""
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		clean, suffix = raw[:i], raw[i:]
	}
	decoded, err := url.PathUnescape(clean)
	if err != nil {
		return "", errs.Input("%s: invalid resource URL %q", context, raw)
	}
	p := filepath.FromSlash(decoded)
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", errs.Input("%s: local resource not found: %s", context, raw)
	}
	return FileURL(p) + suffix, nil
}

func rewriteResources(root *html.Node, file input.SourceFile, fileIndex map[string]int) error {
	base := filepath.Dir(file.Path)
	var firstErr error
	eachElement([]*html.Node{root}, func(n *html.Node) {
		if firstErr != nil {
			return
		}
		if n.Data == "a" {
			rewriteLink(n, fileIndex)
		}
		for _, key := range resourceAttrs[n.Data] {
			v, ok := lookupAttr(n, key)
			if !ok || v == "" || hasSchemeOrNetworkPath(v) || strings.HasPrefix(v, "#") {
				continue
			}
			resolved, err := ResolveLocal(v, base, file.Name)
			if err != nil {
				firstErr = err
				return
			}
			setAttr(n, key, resolved)
		}
		if srcset, ok := lookupAttr(n, "srcset"); ok && srcset != "" {
			out, err := rewriteSrcset(srcset, base, file.Name)
			if err != nil {
				firstErr = err
				return
			}
			setAttr(n, "srcset", out)
		}
	})
	return firstErr
}

func rewriteSrcset(srcset, base, context string) (string, error) {
	parts := strings.Split(srcset, ",")
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 || hasSchemeOrNetworkPath(fields[0]) {
			continue
		}
		resolved, err := ResolveLocal(fields[0], base, context)
		if err != nil {
			return "", err
		}
		fields[0] = resolved
		parts[i] = strings.Join(fields, " ")
	}
	return strings.Join(parts, ", "), nil
}

// rewriteLink rewrites links to other input files as internal anchors.
func rewriteLink(n *html.Node, fileIndex map[string]int) {
	href, ok := lookupAttr(n, "href")
	if !ok || href == "" || hasSchemeOrNetworkPath(href) || strings.HasPrefix(href, "#") {
		return
	}
	clean, fragment, hasFragment := strings.Cut(href, "#")
	if !strings.HasSuffix(strings.ToLower(clean), ".md") {
		return
	}
	decoded, err := url.PathUnescape(clean)
	if err != nil {
		return
	}
	idx, ok := fileIndex[path.Clean(decoded)]
	if !ok {
		return
	}
	if hasFragment {
		setAttr(n, "href", "#"+fragment)
	} else {
		setAttr(n, "href", "#"+FileAnchor(idx))
	}
}

// ---------------------------------------------------------------------------
// Code, Mermaid, math, and tables

func codeLanguage(code *html.Node) string {
	for _, c := range classList(code) {
		if strings.HasPrefix(c, "language-") {
			return strings.TrimPrefix(c, "language-")
		}
	}
	return ""
}

func singleText(n *html.Node) (string, bool) {
	if n.FirstChild == nil {
		return "", true
	}
	if n.FirstChild != n.LastChild || n.FirstChild.Type != html.TextNode {
		return "", false
	}
	return n.FirstChild.Data, true
}

func convertMermaid(root *html.Node, file input.SourceFile) bool {
	var targets []*html.Node
	eachElement([]*html.Node{root}, func(n *html.Node) {
		if n.Data != "pre" || n.FirstChild == nil || n.FirstChild != n.LastChild {
			return
		}
		code := n.FirstChild
		if code.Type == html.ElementNode && code.Data == "code" && codeLanguage(code) == "mermaid" {
			targets = append(targets, n)
		}
	})
	for _, pre := range targets {
		src, ok := singleText(pre.FirstChild)
		if !ok {
			continue
		}
		div := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
			Attr: []html.Attribute{{Key: "class", Val: "pfpdf-mermaid"}}}
		line, _ := strconv.Atoi(getAttr(pre, lineAttr))
		div.Attr = append(div.Attr,
			html.Attribute{Key: "data-file", Val: file.Name},
			html.Attribute{Key: "data-line", Val: strconv.Itoa(line + file.LineOffset)})
		inner := &html.Node{Type: html.ElementNode, Data: "pre", DataAtom: atom.Pre,
			Attr: []html.Attribute{{Key: "class", Val: "mermaid"}}}
		inner.AppendChild(&html.Node{Type: html.TextNode, Data: src})
		div.AppendChild(inner)
		pre.Parent.InsertBefore(div, pre)
		pre.Parent.RemoveChild(pre)
	}
	return len(targets) > 0
}

func highlightCode(root *html.Node, file input.SourceFile, warn func(string)) {
	eachElement([]*html.Node{root}, func(n *html.Node) {
		if n.Data != "code" {
			return
		}
		lang := codeLanguage(n)
		if lang == "" {
			return
		}
		code, ok := singleText(n)
		if !ok {
			return
		}
		nodes, ok := highlight(lang, code)
		if !ok {
			line := 0
			if n.Parent != nil {
				if v, ok := lookupAttr(n.Parent, lineAttr); ok {
					line, _ = strconv.Atoi(v)
				}
			}
			warn(fmt.Sprintf("%s:%d: unknown code block language %q; emitting plain text", file.Name, line+file.LineOffset, lang))
			return
		}
		for n.FirstChild != nil {
			n.RemoveChild(n.FirstChild)
		}
		for _, c := range nodes {
			n.AppendChild(c)
		}
		setAttr(n, "class", getAttr(n, "class")+" hljs")
	})
}

// annotateMath records the source file for in-browser error messages.
func annotateMath(root *html.Node, file input.SourceFile) {
	eachElement([]*html.Node{root}, func(n *html.Node) {
		if !containsClass(n, "pfpdf-math-src") {
			return
		}
		line, _ := strconv.Atoi(getAttr(n, "data-line"))
		setAttr(n, "data-line", strconv.Itoa(line+file.LineOffset))
		setAttr(n, "data-file", file.Name)
	})
}

func decorateTables(root *html.Node) {
	eachElement([]*html.Node{root}, func(n *html.Node) {
		switch n.Data {
		case "table":
			if row := findFirst(n, "tr"); row != nil {
				cols := 0
				for c := row.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && (c.Data == "th" || c.Data == "td") {
						cols++
					}
				}
				if cols >= 8 {
					addClass(n, "pfpdf-table-many-columns")
				}
			}
		case "th", "td":
			text := strings.TrimSpace(elementText(n))
			if text == "" {
				return
			}
			if uniseg.GraphemeClusterCount(text) <= 4 {
				addClass(n, "pfpdf-table-cell-compact")
			} else {
				addClass(n, "pfpdf-table-cell-min-4")
			}
		}
	})
}

func stripLineAttrs(root *html.Node) {
	eachElement([]*html.Node{root}, func(n *html.Node) { removeAttr(n, lineAttr) })
}

// collapsePageBreaks moves root's children into section and converts page
// break markers into `data-pfpdf-page-break` divs. Consecutive markers are
// merged into one, and markers at the start or end of the document are removed.
func collapsePageBreaks(root, section *html.Node) {
	pending := false
	for c := root.FirstChild; c != nil; {
		next := c.NextSibling
		root.RemoveChild(c)
		switch {
		case c.Type == html.ElementNode && containsClass(c, "pfpdf-page-break-marker"):
			pending = true
		case c.Type == html.TextNode && strings.TrimSpace(c.Data) == "":
			if !pending {
				section.AppendChild(c)
			}
		default:
			if pending && section.FirstChild != nil {
				section.AppendChild(&html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
					Attr: []html.Attribute{{Key: "data-pfpdf-page-break", Val: ""}}})
			}
			pending = false
			section.AppendChild(c)
		}
		c = next
	}
}

// ---------------------------------------------------------------------------
// DOM helpers

func eachElement(nodes []*html.Node, fn func(*html.Node)) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			fn(n)
		}
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			walk(c)
			c = next
		}
	}
	for _, n := range nodes {
		walk(n)
	}
}

func findFirst(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.Data == tag {
			return c
		}
		if f := findFirst(c, tag); f != nil {
			return f
		}
	}
	return nil
}

func elementText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case c.Type == html.TextNode:
				b.WriteString(c.Data)
			case c.Type == html.ElementNode && c.Data == "img":
				b.WriteString(getAttr(c, "alt"))
			case c.Type == html.ElementNode:
				walk(c)
			}
		}
	}
	walk(n)
	return b.String()
}

func lookupAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key || a.Namespace != "" && a.Namespace+":"+a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func getAttr(n *html.Node, key string) string {
	v, _ := lookupAttr(n, key)
	return v
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && a.Key == key || a.Namespace != "" && a.Namespace+":"+a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			continue
		}
		out = append(out, a)
	}
	n.Attr = out
}

func containsClass(n *html.Node, cls string) bool {
	for _, c := range classList(n) {
		if c == cls {
			return true
		}
	}
	return false
}

func hasClass(root *html.Node, cls string) bool {
	found := false
	eachElement([]*html.Node{root}, func(n *html.Node) {
		if !found && containsClass(n, cls) {
			found = true
		}
	})
	return found
}

func addClass(n *html.Node, cls string) {
	if v := getAttr(n, "class"); v != "" {
		setAttr(n, "class", v+" "+cls)
	} else {
		setAttr(n, "class", cls)
	}
}
