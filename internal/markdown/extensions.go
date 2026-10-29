package markdown

import (
	"bytes"
	"fmt"
	"strconv"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// newMarkdown builds a goldmark instance with GFM, footnotes, math, CJK
// emphasis rules, and md2pdf directives. idPrefix keeps footnote IDs unique
// when several files are concatenated.
func newMarkdown(idPrefix string, st *citeState) goldmark.Markdown {
	inline := []util.PrioritizedValue{}
	for _, v := range parser.DefaultInlineParsers() {
		if isEmphasisParser(v.Value) {
			v = util.Prioritized(&cjkEmphasisParser{}, v.Priority)
		}
		inline = append(inline, v)
	}
	p := parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(inline...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
	return goldmark.New(
		goldmark.WithParser(p),
		goldmark.WithExtensions(
			extension.GFM,
			extension.NewFootnote(extension.WithFootnoteIDPrefix(idPrefix)),
			extension.NewCJK(extension.WithEastAsianLineBreaks(extension.EastAsianLineBreaksCSS3Draft)),
			&directives{},
			&citations{st: st},
		),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
}

func isEmphasisParser(v any) bool {
	t, ok := v.(interface{ Trigger() []byte })
	return ok && bytes.Equal(t.Trigger(), []byte{'*', '_'})
}

// ---------------------------------------------------------------------------
// CJK-friendly emphasis
//
// CommonMark refuses `これは**「重要」**です` because the delimiter runs sit
// between a letter and punctuation. Following the markdown-cjk-friendly
// proposal, a CJK character on the outer side of a run is treated like
// punctuation, so such runs can still open or close emphasis.

type emphasisDelimiterProcessor struct{}

func (p *emphasisDelimiterProcessor) IsDelimiter(b byte) bool { return b == '*' || b == '_' }

func (p *emphasisDelimiterProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (p *emphasisDelimiterProcessor) OnMatch(consumes int) ast.Node { return ast.NewEmphasis(consumes) }

var emphasisProcessor = &emphasisDelimiterProcessor{}

type cjkEmphasisParser struct{}

func (s *cjkEmphasisParser) Trigger() []byte { return []byte{'*', '_'} }

func (s *cjkEmphasisParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := scanCJKDelimiter(line, before, emphasisProcessor)
	if node == nil {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

func scanCJKDelimiter(line []byte, before rune, processor parser.DelimiterProcessor) *parser.Delimiter {
	c := line[0]
	if !processor.IsDelimiter(c) {
		return nil
	}
	j := 0
	for j < len(line) && line[j] == c {
		j++
	}
	after := ' '
	if j != len(line) {
		after = util.ToRune(line, j)
	}
	beforePunct := util.IsPunctRune(before)
	beforeSpace := util.IsSpaceRune(before)
	afterPunct := util.IsPunctRune(after)
	afterSpace := util.IsSpaceRune(after)
	beforeCJK := isCJK(before)
	afterCJK := isCJK(after)

	isLeft := !afterSpace && (!afterPunct || beforeSpace || beforePunct || beforeCJK)
	isRight := !beforeSpace && (!beforePunct || afterSpace || afterPunct || afterCJK)

	var canOpen, canClose bool
	if c == '_' {
		canOpen = isLeft && (!isRight || beforePunct)
		canClose = isRight && (!isLeft || afterPunct)
	} else {
		canOpen = isLeft
		canClose = isRight
	}
	return parser.NewDelimiter(canOpen, canClose, j, c, processor)
}

func isCJK(r rune) bool {
	switch {
	case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo):
		return true
	case r >= 0x3000 && r <= 0x30FF: // CJK symbols and punctuation, kana
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // halfwidth and fullwidth forms
		return true
	case r >= 0x31F0 && r <= 0x31FF, r >= 0x3200 && r <= 0x33FF:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Math ($...$, $$...$$) and page breaks (`___`)

// KindMath is the AST kind for inline and display math.
var KindMath = ast.NewNodeKind("Math")

// Math is a TeX expression rendered later by MathJax.
type Math struct {
	ast.BaseInline
	TeX     []byte
	Display bool
}

func (n *Math) Kind() ast.NodeKind { return KindMath }

func (n *Math) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"TeX": string(n.TeX)}, nil)
}

// KindMathBlock is the AST kind for $$-fenced blocks.
var KindMathBlock = ast.NewNodeKind("MathBlock")

// MathBlock is a display math block delimited by $$ lines.
type MathBlock struct {
	ast.BaseBlock
}

func (n *MathBlock) Kind() ast.NodeKind { return KindMathBlock }
func (n *MathBlock) IsRaw() bool        { return true }
func (n *MathBlock) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// KindPageBreak is the AST kind for the `___` page-break directive.
var KindPageBreak = ast.NewNodeKind("PageBreak")

// PageBreak represents a top-level thematic break written exactly as `___`.
type PageBreak struct {
	ast.BaseBlock
}

func (n *PageBreak) Kind() ast.NodeKind { return KindPageBreak }
func (n *PageBreak) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

type inlineMathParser struct{}

func (p *inlineMathParser) Trigger() []byte { return []byte{'$'} }

// Parse follows Pandoc's rules for single dollars: the opening $ must be
// followed by non-space, and the closing $ preceded by non-space and not
// followed by a digit, so "$5 and $10" stays text.
func (p *inlineMathParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	n := 0
	for n < len(line) && line[n] == '$' {
		n++
	}
	if n > 2 {
		return nil
	}
	rest := line[n:]
	if len(rest) == 0 || (n == 1 && isSpaceByte(rest[0])) {
		return nil
	}
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '\\':
			i++
			continue
		case '$':
		default:
			continue
		}
		run := 0
		for i+run < len(rest) && rest[i+run] == '$' {
			run++
		}
		if run != n {
			i += run - 1
			continue
		}
		body := rest[:i]
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		if n == 1 {
			if isSpaceByte(body[len(body)-1]) {
				continue
			}
			if next := i + run; next < len(rest) && rest[next] >= '0' && rest[next] <= '9' {
				continue
			}
		}
		block.Advance(n + i + run)
		return &Math{TeX: append([]byte(nil), body...), Display: n == 2}
	}
	return nil
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

type mathBlockParser struct{}

func (p *mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (p *mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, seg := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 {
		return nil, parser.NoChildren
	}
	trimmed := bytes.TrimSpace(line[pos:])
	if !bytes.HasPrefix(trimmed, []byte("$$")) {
		return nil, parser.NoChildren
	}
	inner := bytes.TrimSpace(trimmed[2:])
	node := &MathBlock{}
	if len(inner) >= 2 && bytes.HasSuffix(inner, []byte("$$")) {
		// Single-line form: $$ x $$
		body := bytes.TrimSpace(inner[:len(inner)-2])
		if bytes.Contains(body, []byte("$$")) {
			return nil, parser.NoChildren
		}
		start := seg.Start + bytes.Index(line, body)
		node.Lines().Append(text.NewSegment(start, start+len(body)))
		reader.AdvanceToEOL()
		return node, parser.Close
	}
	if len(inner) > 0 {
		start := seg.Start + bytes.Index(line, inner)
		node.Lines().Append(text.NewSegment(start, start+len(inner)))
	}
	reader.AdvanceToEOL()
	return node, parser.NoChildren
}

func (p *mathBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	line, seg := reader.PeekLine()
	if util.IsBlank(line) && len(line) == 0 {
		return parser.Close
	}
	trimmed := bytes.TrimSpace(line)
	if bytes.HasSuffix(trimmed, []byte("$$")) {
		body := bytes.TrimSpace(trimmed[:len(trimmed)-2])
		if len(body) > 0 {
			start := seg.Start + bytes.Index(line, body)
			node.Lines().Append(text.NewSegment(start, start+len(body)))
		}
		reader.AdvanceToEOL()
		return parser.Close
	}
	node.Lines().Append(seg)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (p *mathBlockParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}
func (p *mathBlockParser) CanInterruptParagraph() bool                                { return true }
func (p *mathBlockParser) CanAcceptIndentedLine() bool                                { return false }

// citations wires in \cite{...} and \printbibliography.
type citations struct{ st *citeState }

func (c *citations) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(util.Prioritized(&citationParser{st: c.st}, 90)),
		parser.WithASTTransformers(util.Prioritized(&citationTransformer{st: c.st}, 110)),
	)
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(&citationRenderer{}, 100)))
}

// directives wires in math, page breaks, heading markers, and line attributes.
type directives struct{}

func (d *directives) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(util.Prioritized(&inlineMathParser{}, 150)),
		parser.WithBlockParsers(util.Prioritized(&mathBlockParser{}, 90)),
		parser.WithASTTransformers(util.Prioritized(&directiveTransformer{}, 100)),
	)
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(&directiveRenderer{}, 100)))
}

type directiveTransformer struct{}

func (t *directiveTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	// Page break: a top-level thematic break whose source line is exactly `___`.
	for c := doc.FirstChild(); c != nil; {
		next := c.NextSibling()
		if c.Kind() == ast.KindThematicBreak && c.Pos() >= 0 && sourceLine(source, c.Pos()) == "___" {
			pb := &PageBreak{}
			doc.ReplaceChild(doc, c, pb)
		}
		c = next
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindHeading:
			n.SetAttributeString(headingMarker, []byte("1"))
		}
		return ast.WalkContinue, nil
	})
}

func sourceLine(source []byte, pos int) string {
	start := bytes.LastIndexByte(source[:pos], '\n') + 1
	end := bytes.IndexByte(source[pos:], '\n')
	if end < 0 {
		end = len(source)
	} else {
		end += pos
	}
	return string(bytes.TrimRight(source[start:end], "\r"))
}

func lineOf(source []byte, pos int) int {
	if pos < 0 {
		return 0
	}
	return bytes.Count(source[:pos], []byte{'\n'}) + 1
}

type directiveRenderer struct{}

func (r *directiveRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindMath, r.renderMath)
	reg.Register(KindMathBlock, r.renderMathBlock)
	reg.Register(KindPageBreak, r.renderPageBreak)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCode)
}

func (r *directiveRenderer) renderMath(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	m := n.(*Math)
	display := "0"
	if m.Display {
		display = "1"
	}
	line := 0
	if p := n.Parent(); p != nil {
		line = blockLine(source, p)
	}
	fmt.Fprintf(w, `<span class="pfpdf-math-src" data-display="%s" data-line="%d">`, display, line)
	w.Write(util.EscapeHTML(m.TeX))
	w.WriteString("</span>")
	return ast.WalkSkipChildren, nil
}

func (r *directiveRenderer) renderMathBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	fmt.Fprintf(w, `<div class="pfpdf-math-src" data-display="1" data-line="%d">`, blockLine(source, n))
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		w.Write(util.EscapeHTML(bytes.TrimRight(seg.Value(source), "\r\n")))
		if i < lines.Len()-1 {
			w.WriteByte('\n')
		}
	}
	w.WriteString("</div>\n")
	return ast.WalkSkipChildren, nil
}

func (r *directiveRenderer) renderPageBreak(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		w.WriteString(`<div class="pfpdf-page-break-marker"></div>` + "\n")
	}
	return ast.WalkSkipChildren, nil
}

func (r *directiveRenderer) renderFencedCode(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	fc := n.(*ast.FencedCodeBlock)
	w.WriteString(`<pre ` + lineAttr + `="` + strconv.Itoa(blockLine(source, n)) + `"><code`)
	if lang := fc.Language(source); lang != nil {
		w.WriteString(` class="language-`)
		w.Write(util.EscapeHTML(lang))
		w.WriteString(`"`)
	}
	w.WriteString(">")
	lines := fc.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		w.Write(util.EscapeHTML(seg.Value(source)))
	}
	w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}

// blockLine returns the 1-based source line of the nearest block ancestor.
func blockLine(source []byte, n ast.Node) int {
	for ; n != nil; n = n.Parent() {
		if n.Type() != ast.TypeBlock {
			continue
		}
		if n.Pos() >= 0 {
			return lineOf(source, n.Pos())
		}
		if n.Lines().Len() > 0 {
			return lineOf(source, n.Lines().At(0).Start)
		}
	}
	return 0
}

const (
	headingMarker = "data-md2pdf-heading"
	lineAttr      = "data-md2pdf-line"
)
