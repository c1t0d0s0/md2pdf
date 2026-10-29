// Package document resolves templates and assembles the final HTML document:
// template slots, the TOC, stylesheets, fonts, and browser-side scripts for
// math, Mermaid, and readiness detection.
package document

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/c1t0d0s0/md2pdf/internal/assets"
	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/input"
	"github.com/c1t0d0s0/md2pdf/internal/markdown"
)

const slotAttr = "data-pfpdf-slot"

var knownSlots = map[string]bool{
	"content": true, "title": true, "author": true, "series": true,
	"date": true, "confidential": true, "toc": true, "logo": true,
}

// Template is a parsed template directory.
type Template struct {
	Dir         string
	Bundled     bool
	HTMLPath    string
	StylePath   string
	PrintPath   string // optional paged-media stylesheet
	root        *html.Node
	ReservedIDs []string
}

// LoadTemplate resolves a bundled preset (extracted under workDir) or a
// custom template directory.
func LoadTemplate(spec, workDir string) (*Template, error) {
	t := &Template{}
	wrap := func(err error) error { return err }
	if assets.IsTemplate(spec) {
		dst := filepath.Join(workDir, "templates")
		if _, err := os.Stat(dst); err != nil {
			if err := assets.Extract(assets.TemplateFS, dst); err != nil {
				return nil, errs.Runtime("cannot extract bundled templates: %v", err)
			}
		}
		t.Dir, t.Bundled = filepath.Join(dst, spec), true
		wrap = func(err error) error { return errs.Runtime("bundled template is broken: %v", err) }
	} else {
		abs, err := filepath.Abs(spec)
		if err != nil {
			return nil, errs.Input("template: %v", err)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, errs.Input("template directory not found: %s (bundled presets: %s)", spec, strings.Join(assets.TemplateNames(), ", "))
		}
		t.Dir = abs
	}
	regular := func(name string) (string, bool) {
		p := filepath.Join(t.Dir, name)
		st, err := os.Stat(p)
		return p, err == nil && st.Mode().IsRegular()
	}
	var ok bool
	if t.HTMLPath, ok = regular("template.html"); !ok {
		return nil, wrap(errs.Input("template file missing: %s", t.HTMLPath))
	}
	if t.StylePath, ok = regular("style.css"); !ok {
		return nil, wrap(errs.Input("template file missing: %s", t.StylePath))
	}
	// print.css. pfpdf templates call the same file vivliostyle.css.
	if p, ok := regular("print.css"); ok {
		t.PrintPath = p
	} else if p, ok := regular("vivliostyle.css"); ok {
		t.PrintPath = p
	}
	data, err := os.ReadFile(t.HTMLPath)
	if err != nil {
		return nil, wrap(errs.Input("template: cannot read %s: %v", t.HTMLPath, err))
	}
	if t.root, err = html.Parse(bytes.NewReader(data)); err != nil {
		return nil, wrap(errs.Input("template: cannot parse %s: %v", t.HTMLPath, err))
	}
	if _, err := findSlots(t.root); err != nil {
		return nil, wrap(err)
	}
	walk(t.root, func(n *html.Node) {
		if id := attr(n, "id"); id != "" {
			t.ReservedIDs = append(t.ReservedIDs, id)
		}
	})
	return t, nil
}

func findSlots(root *html.Node) (map[string]*html.Node, error) {
	slots := map[string]*html.Node{}
	var err error
	walk(root, func(n *html.Node) {
		name, ok := lookup(n, slotAttr)
		if !ok || err != nil {
			return
		}
		switch {
		case !knownSlots[name]:
			err = errs.Input("template: unknown slot: %s", name)
		case slots[name] != nil:
			err = errs.Input("template: duplicate slot: %s", name)
		case name == "logo" && n.Data != "img":
			err = errs.Input("template: logo slot must be an <img> element")
		default:
			slots[name] = n
		}
	})
	if err != nil {
		return nil, err
	}
	if slots["content"] == nil {
		return nil, errs.Input("template: required slot \"content\" is missing")
	}
	return slots, nil
}

// Logo selects the cover logo.
type Logo struct {
	None bool   // remove the template's logo
	Path string // explicit logo file; empty means use the template default
}

// Options are the inputs to Assemble.
type Options struct {
	Metadata input.Metadata
	Body     *markdown.Body
	Template *Template
	Logo     Logo
	TOC      bool
	FontDirs []string
	WorkDir  string
	Warn     func(string)
}

// Result is the assembled document.
type Result struct {
	Path     string // absolute path of document.html in the work directory
	HasTOC   bool
	TOCPages bool // TOC shows page numbers, which require a second pass
}

// Assemble fills the template and writes document.html and the generated
// stylesheets to opts.WorkDir.
func Assemble(opts Options) (*Result, error) {
	md, t := opts.Metadata, opts.Template
	root := t.root
	slots, err := findSlots(root)
	if err != nil {
		return nil, err
	}
	htmlEl, head, body := findFirst(root, "html"), findFirst(root, "head"), findFirst(root, "body")
	if htmlEl == nil || head == nil || body == nil {
		return nil, errs.Input("template: template.html must declare html, head, and body")
	}
	templateNodes := map[*html.Node]bool{}
	walk(root, func(n *html.Node) { templateNodes[n] = true })

	setAttr(htmlEl, "lang", md.Lang)
	setAttr(htmlEl, "dir", md.Dir)

	plainTitle := strings.Join(strings.Fields(strings.ReplaceAll(md.Title.PlainText, "\n", " ")), " ")
	if titleEl := findFirst(head, "title"); titleEl != nil {
		setText(titleEl, plainTitle)
	} else {
		titleEl = elem("title")
		titleEl.AppendChild(text(plainTitle))
		head.InsertBefore(titleEl, head.FirstChild)
	}

	if s := slots["title"]; s != nil {
		clearChildren(s)
		for i, line := range md.Title.Lines {
			if i > 0 {
				s.AppendChild(elem("br"))
			}
			if line != "" {
				s.AppendChild(text(line))
			}
		}
	}
	fillOrRemove := func(name, value string, keep bool) {
		if s := slots[name]; s != nil {
			if !keep {
				remove(s)
				slots[name] = nil
				return
			}
			setText(s, value)
		}
	}
	fillOrRemove("author", md.Author, md.Author != "")
	fillOrRemove("series", md.Series, md.Series != "")
	fillOrRemove("date", md.Date, true)
	fillOrRemove("confidential", "Confidential", md.Confidential)

	if s := slots["logo"]; s != nil || opts.Logo.Path != "" {
		switch {
		case opts.Logo.Path != "":
			if s == nil {
				return nil, errs.Input("an explicit logo was given but the template has no logo slot")
			}
			if st, err := os.Stat(opts.Logo.Path); err != nil || !st.Mode().IsRegular() {
				return nil, errs.Input("logo file not found: %s", opts.Logo.Path)
			}
			setAttr(s, "src", markdown.FileURL(opts.Logo.Path))
			delete(templateNodes, s) // already absolute
		case opts.Logo.None || attr(s, "src") == "":
			remove(s)
			slots["logo"] = nil
		}
		if s := slots["logo"]; s != nil {
			if _, ok := lookup(s, "alt"); !ok {
				setAttr(s, "alt", "")
			}
		}
	}

	// Table of contents.
	res := &Result{Path: filepath.Join(opts.WorkDir, "document.html")}
	var tocEl *html.Node
	if opts.TOC && len(opts.Body.TOC) > 0 {
		tocEl = tocElement(opts.Body.TOC, md.Lang)
		res.HasTOC = true
	}
	if s := slots["toc"]; s != nil {
		if tocEl != nil {
			clearChildren(s)
			s.AppendChild(tocEl)
		} else {
			remove(s)
			slots["toc"] = nil
		}
	}
	content := slots["content"]
	clearChildren(content)
	if tocEl != nil && slots["toc"] == nil {
		content.AppendChild(tocEl)
	}
	if tocEl != nil {
		content.AppendChild(tocContinuationReset())
	}
	for _, sec := range opts.Body.Sections {
		content.AppendChild(sec)
	}

	// Template-relative resources.
	if err := rewriteTemplateResources(root, t.Dir, templateNodes); err != nil {
		return nil, err
	}

	// Stylesheets.
	genDir := filepath.Join(opts.WorkDir, "generated")
	if err := os.MkdirAll(genDir, 0o700); err != nil {
		return nil, errs.Runtime("cannot create work directory: %v", err)
	}
	fontsCSS, err := fontFaceCSS(genDir, opts.FontDirs, opts.Warn)
	if err != nil {
		return nil, err
	}
	hljs, err := readAsset(assets.Highlight)
	if err != nil {
		return nil, err
	}
	style, err := loadCSS(t.StylePath, map[string]bool{})
	if err != nil {
		return nil, templateErr(t, err)
	}
	print := ""
	if t.PrintPath != "" {
		if print, err = loadCSS(t.PrintPath, map[string]bool{}); err != nil {
			return nil, templateErr(t, err)
		}
	}
	cctx := cssContext{title: plainTitle, slotText: func(classes []string) (string, bool) {
		for _, s := range slots {
			if s == nil {
				continue
			}
			for _, c := range classes {
				if hasClass(s, c) {
					return textContent(s), true
				}
			}
		}
		return "", false
	}}
	named := map[string]string{}
	collectNamedStrings([]string{style, print}, cctx, named)
	styleT := translateCSS(style, cctx, named)
	printT := translateCSS(print, cctx, named)
	res.TOCPages = res.HasTOC && (styleT.usesPages || printT.usesPages)
	if tocEl != nil && (styleT.leader || printT.leader) {
		addLeaders(tocEl)
	}

	pageCSS := "@page { size: " + md.PageSize + "; }\n" + generatedCSS
	if styleT.leader || printT.leader {
		pageCSS += leaderCSS
	}
	files := []struct{ name, css string }{
		{"fonts.css", fontsCSS},
		{"hljs.css", string(hljs)},
		{"style.css", styleT.css},
		{"print.css", printT.css},
		{"page.css", pageCSS},
	}
	var links []*html.Node
	links = append(links, elemAttr("meta", "charset", "utf-8"), script("", initScript))
	for _, f := range files {
		p := filepath.Join(genDir, f.name)
		if err := os.WriteFile(p, []byte(f.css), 0o600); err != nil {
			return nil, errs.Runtime("cannot write %s: %v", p, err)
		}
		links = append(links, elemAttr("link", "rel", "stylesheet", "href", "generated/"+f.name))
	}
	links = append(links, elemAttr("meta", "name", "referrer", "content", "no-referrer"))
	// Drop the template's charset meta to avoid duplicates and put ours first.
	for c := head.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && c.Data == "meta" {
			if _, ok := lookup(c, "charset"); ok {
				head.RemoveChild(c)
			}
		}
		c = next
	}
	first := head.FirstChild
	for _, l := range links {
		head.InsertBefore(l, first)
	}

	// Scripts: libraries are copied only when the document needs them.
	if opts.Body.NeedsMath {
		if err := copyAsset(assets.MathJax, filepath.Join(genDir, "mathjax.js")); err != nil {
			return nil, err
		}
		body.AppendChild(script("", mathJaxConfig))
		body.AppendChild(script("generated/mathjax.js", ""))
	}
	if opts.Body.NeedsMermaid {
		if err := copyAsset(assets.Mermaid, filepath.Join(genDir, "mermaid.js")); err != nil {
			return nil, err
		}
		body.AppendChild(script("generated/mermaid.js", ""))
	}
	body.AppendChild(script("", readinessScript))

	walk(root, func(n *html.Node) { removeAttr(n, slotAttr) })

	var buf bytes.Buffer
	if err := html.Render(&buf, root); err != nil {
		return nil, errs.Runtime("cannot serialize document: %v", err)
	}
	if err := os.WriteFile(res.Path, buf.Bytes(), 0o600); err != nil {
		return nil, errs.Runtime("cannot write document: %v", err)
	}
	return res, nil
}

func templateErr(t *Template, err error) error {
	if t.Bundled {
		return errs.Runtime("bundled template stylesheet is broken: %v", err)
	}
	return err
}

func readAsset(name string) ([]byte, error) {
	data, err := assetsReadFile(name)
	if err != nil {
		return nil, errs.Runtime("bundled asset is missing: %s", name)
	}
	return data, nil
}

func copyAsset(name, dst string) error {
	data, err := readAsset(name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return errs.Runtime("cannot write %s: %v", dst, err)
	}
	return nil
}

var templateResourceAttrs = map[string][]string{
	"img": {"src"}, "script": {"src"}, "link": {"href"}, "source": {"src"},
	"video": {"src", "poster"}, "audio": {"src"}, "object": {"data"},
	"image": {"href", "xlink:href"}, "use": {"href", "xlink:href"},
}

func rewriteTemplateResources(root *html.Node, dir string, include map[*html.Node]bool) error {
	var firstErr error
	walk(root, func(n *html.Node) {
		if firstErr != nil || !include[n] {
			return
		}
		for _, key := range templateResourceAttrs[n.Data] {
			v, ok := lookup(n, key)
			if !ok || v == "" || hasScheme(v) || strings.HasPrefix(v, "#") {
				continue
			}
			resolved, err := markdown.ResolveLocal(v, dir, "template")
			if err != nil {
				firstErr = err
				return
			}
			setAttr(n, key, resolved)
		}
	})
	return firstErr
}

func tocLabel(lang string) string {
	if lang == "ja" || strings.HasPrefix(lang, "ja-") {
		return "目次"
	}
	return "Contents"
}

func tocElement(entries []markdown.TOCEntry, lang string) *html.Node {
	nav := elemAttr("nav", "class", "pfpdf-toc", "role", "doc-toc")
	h := elemAttr("h2", "class", "pfpdf-toc-title")
	h.AppendChild(text(tocLabel(lang)))
	nav.AppendChild(h)
	marker := elemAttr("span", "aria-hidden", "true", "class", "pfpdf-toc-continuation-marker")
	nav.AppendChild(marker)
	list := elemAttr("ol", "class", "pfpdf-toc-list")
	for _, e := range entries {
		li := elemAttr("li", "class", "pfpdf-toc-depth-"+string(rune('0'+e.Depth)))
		a := elemAttr("a", "href", "#"+e.ID)
		label := elemAttr("span", "class", "pfpdf-toc-label")
		txt := elemAttr("span", "class", "pfpdf-toc-text")
		txt.AppendChild(text(e.Text))
		label.AppendChild(txt)
		a.AppendChild(label)
		li.AppendChild(a)
		list.AppendChild(li)
	}
	nav.AppendChild(list)
	return nav
}

func addLeaders(toc *html.Node) {
	walk(toc, func(n *html.Node) {
		if n.Data == "a" && n.Parent != nil && n.Parent.Data == "li" {
			n.AppendChild(elemAttr("span", "class", "md2pdf-toc-leader", "aria-hidden", "true"))
		}
	})
}

func tocContinuationReset() *html.Node {
	return elemAttr("span", "aria-hidden", "true", "class", "pfpdf-toc-continuation-marker pfpdf-toc-continuation-reset")
}

// generatedCSS holds zero-specificity font fallbacks for templates that
// specify no fonts, structural rules the templates assume, and Chromium
// adjustments. inter-character justification stretches Latin words and
// inline code too, while "auto" still justifies CJK text.
const generatedCSS = `:where(html) { font-family: "Noto Sans JP", sans-serif; }
:where(code, kbd, pre, samp) { font-family: "Noto Sans Mono", "Noto Sans JP", monospace; }
.pfpdf-toc-continuation-marker { display: none; }
[data-pfpdf-page-break] { break-before: page; height: 0; margin: 0; padding: 0; }
.pfpdf-mermaid { break-inside: avoid; margin: 1.25em auto; text-align: center; }
.pfpdf-mermaid svg { display: inline-block; height: auto; max-width: 100%; }
.pfpdf-mermaid > pre.mermaid { visibility: hidden; }
.pfpdf-toc a[data-md2pdf-page]::after { white-space: nowrap; }
:is(th, td) :not(pre) > code { overflow-wrap: break-word; word-break: normal; }
p, li, dd { text-justify: auto; }
`

// leaderCSS replaces CSS leader() with a stretching dotted element.
const leaderCSS = `.pfpdf-toc a { display: flex; align-items: baseline; }
.pfpdf-toc a > .pfpdf-toc-label { flex: 0 1 auto; min-width: 0; }
.pfpdf-toc a > .md2pdf-toc-leader {
  align-self: baseline; border-bottom: 0.9pt dotted currentColor; flex: 1 1 1.5em;
  margin: 0 0.35em; min-width: 1.5em; opacity: 0.45; order: 1; position: relative; top: -0.25em;
}
.pfpdf-toc a::after { flex: none; order: 2; padding-inline-start: 0; }
`

// ---------------------------------------------------------------------------
// DOM helpers

func walk(n *html.Node, fn func(*html.Node)) {
	if n.Type == html.ElementNode {
		fn(n)
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		walk(c, fn)
		c = next
	}
}

func findFirst(n *html.Node, tag string) *html.Node {
	var found *html.Node
	walk(n, func(e *html.Node) {
		if found == nil && e.Data == tag {
			found = e
		}
	})
	return found
}

func lookup(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		k := a.Key
		if a.Namespace != "" {
			k = a.Namespace + ":" + a.Key
		}
		if k == key {
			return a.Val, true
		}
	}
	return "", false
}

func attr(n *html.Node, key string) string {
	v, _ := lookup(n, key)
	return v
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		k := a.Key
		if a.Namespace != "" {
			k = a.Namespace + ":" + a.Key
		}
		if k == key {
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

func hasClass(n *html.Node, cls string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == cls {
			return true
		}
	}
	return false
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			} else {
				rec(c)
			}
		}
	}
	rec(n)
	return b.String()
}

func clearChildren(n *html.Node) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
}

func setText(n *html.Node, s string) {
	clearChildren(n)
	n.AppendChild(text(s))
}

func remove(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func text(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }

func elem(tag string) *html.Node {
	return &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
}

func elemAttr(tag string, kv ...string) *html.Node {
	n := elem(tag)
	for i := 0; i+1 < len(kv); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
	}
	return n
}

func script(src, code string) *html.Node {
	s := elem("script")
	if src != "" {
		setAttr(s, "src", src)
	}
	if code != "" {
		s.AppendChild(text(code))
	}
	return s
}
