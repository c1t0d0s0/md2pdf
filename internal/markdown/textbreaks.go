package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/net/html"
)

// Long unbreakable strings (URLs, identifiers, hashes) overflow narrow columns.
// insertWordBreaks adds <wbr> break opportunities at meaningful positions
// without changing the displayed text.

const (
	wordBreakThreshold = 16
	minFragmentLength  = 4
)

var (
	excludedTags = set("pre", "code", "kbd", "samp", "script", "style", "textarea", "svg", "math")
	excludedCls  = set("pfpdf-math-src", "pfpdf-math-inline", "pfpdf-math-display")
	breakTags    = set("br", "wbr")
	replacedTags = set("audio", "canvas", "embed", "iframe", "img", "input", "object", "select", "video")
	blockTags    = set("address", "article", "aside", "blockquote", "body", "dd", "details", "dialog",
		"div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form",
		"h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr", "html", "li",
		"main", "nav", "ol", "p", "section", "summary", "table", "tbody", "td", "tfoot",
		"th", "thead", "tr", "ul")
	urlSymbols = "!#$%&'*+,-./:;=?@[\\]_~"
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

type grapheme struct {
	value  string
	node   *html.Node
	offset int // byte offset within node.Data
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func isRunGrapheme(g string) bool {
	if g == "​" || g == "­" {
		return false
	}
	r := firstRune(g)
	if unicode.IsSpace(r) {
		return false
	}
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return false
	}
	return unicode.In(r, unicode.Letter, unicode.Mark, unicode.Number) || (len(g) == 1 && strings.Contains(urlSymbols, g))
}

func isHex(values []string, i int) bool {
	if i < 0 || i >= len(values) || len(values[i]) != 1 {
		return false
	}
	c := values[i][0]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func at(values []string, i int) string {
	if i < 0 || i >= len(values) {
		return ""
	}
	return values[i]
}

func isForbiddenBoundary(v []string, p int) bool {
	// Do not split %HH percent-encoded octets or the IPv6 "::".
	if at(v, p-1) == "%" && isHex(v, p) && isHex(v, p+1) {
		return true
	}
	if at(v, p-2) == "%" && isHex(v, p-1) && isHex(v, p) {
		return true
	}
	if at(v, p-1) == ":" && at(v, p) == ":" {
		return true
	}
	// Do not split "://"; the point after the second slash remains a preferred break.
	if at(v, p-1) == ":" && at(v, p) == "/" {
		return true
	}
	if at(v, p-2) == ":" && at(v, p-1) == "/" && at(v, p) == "/" {
		return true
	}
	return false
}

func is(s string, tables ...*unicode.RangeTable) bool {
	return s != "" && unicode.In(firstRune(s), tables...)
}

// breakPriority returns smaller values for better break positions, or -1
// if breaking just before position is forbidden.
func breakPriority(v []string, p int) int {
	if p <= 0 || p >= len(v) || isForbiddenBoundary(v, p) {
		return -1
	}
	before, after := v[p-1], v[p]
	if strings.Contains("/?&#;=", before) {
		return 0
	}
	if strings.Contains(".@:-_~+,", before) {
		return 1
	}
	if (is(before, unicode.Ll) && is(after, unicode.Lu)) ||
		(is(before, unicode.Letter) && is(after, unicode.Number)) ||
		(is(before, unicode.Number) && is(after, unicode.Letter)) ||
		(is(before, unicode.Lu) && is(after, unicode.Lu) && is(at(v, p+1), unicode.Letter) && is(at(v, p+1), unicode.Ll)) {
		return 2
	}
	return 3
}

type pathState struct {
	valid                      bool
	cuts, priority, sq, posSum int
	previous                   int
}

func less(a, b pathState) bool {
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if a.cuts != b.cuts {
		return a.cuts < b.cuts
	}
	if a.sq != b.sq {
		return a.sq < b.sq
	}
	return a.posSum < b.posSum
}

// selectWordBreaks deterministically chooses break positions in O(n * threshold).
func selectWordBreaks(v []string) []int {
	n := len(v)
	if n < wordBreakThreshold {
		return nil
	}
	states := make([]pathState, n+1)
	states[0] = pathState{valid: true, previous: -1}
	for end := minFragmentLength; end <= n; end++ {
		for start := max(0, end-wordBreakThreshold); start <= end-minFragmentLength; start++ {
			prev := states[start]
			pr := 0
			if start > 0 {
				pr = breakPriority(v, start)
			}
			if !prev.valid || pr < 0 {
				continue
			}
			seg := end - start
			cand := pathState{valid: true, priority: prev.priority + pr, sq: prev.sq + seg*seg,
				posSum: prev.posSum + start, previous: start, cuts: prev.cuts}
			if start > 0 {
				cand.cuts++
			}
			if !states[end].valid || less(cand, states[end]) {
				states[end] = cand
			}
		}
	}
	final := states[n]
	if !final.valid {
		var cuts []int
		for p := wordBreakThreshold; p < n; p += wordBreakThreshold {
			cuts = append(cuts, p)
		}
		return cuts
	}
	if n == wordBreakThreshold && final.cuts == 0 {
		best := minFragmentLength
		for p := minFragmentLength + 1; p <= n-minFragmentLength; p++ {
			pr, bp := breakPriority(v, p), breakPriority(v, best)
			if pr >= 0 && (bp < 0 || pr < bp || (pr == bp && abs(n-2*p) < abs(n-2*best))) {
				best = p
			}
		}
		return []int{best}
	}
	var cuts []int
	for cur := n; cur > 0; {
		st := states[cur]
		if st.previous > 0 {
			cuts = append(cuts, st.previous)
		}
		cur = st.previous
	}
	for i, j := 0, len(cuts)-1; i < j; i, j = i+1, j-1 {
		cuts[i], cuts[j] = cuts[j], cuts[i]
	}
	return cuts
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func classList(n *html.Node) []string {
	return strings.Fields(getAttr(n, "class"))
}

func isExcluded(n *html.Node) bool {
	if excludedTags[n.Data] {
		return true
	}
	for _, c := range classList(n) {
		if excludedCls[c] {
			return true
		}
	}
	if v, ok := lookupAttr(n, "contenteditable"); ok && strings.ToLower(v) != "false" {
		return true
	}
	return false
}

// insertWordBreaks inserts <wbr> elements into long visible strings under root.
func insertWordBreaks(root *html.Node) {
	var run []grapheme
	insertions := map[*html.Node]map[int]bool{}
	var order []*html.Node

	flush := func() {
		if len(run) >= wordBreakThreshold {
			values := make([]string, len(run))
			for i, g := range run {
				values[i] = g.value
			}
			for _, cut := range selectWordBreaks(values) {
				g := run[cut]
				if insertions[g.node] == nil {
					insertions[g.node] = map[int]bool{}
					order = append(order, g.node)
				}
				insertions[g.node][g.offset] = true
			}
		}
		run = run[:0]
	}

	var walk func(parent *html.Node)
	walk = func(parent *html.Node) {
		for c := parent.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				gr := uniseg.NewGraphemes(c.Data)
				for gr.Next() {
					from, _ := gr.Positions()
					s := gr.Str()
					if !isRunGrapheme(s) {
						flush()
						continue
					}
					run = append(run, grapheme{value: s, node: c, offset: from})
				}
			case html.ElementNode:
				if isExcluded(c) || breakTags[c.Data] || replacedTags[c.Data] {
					flush()
					continue
				}
				if blockTags[c.Data] {
					flush()
					walk(c)
					flush()
				} else {
					walk(c)
				}
			default:
				flush()
			}
		}
	}
	walk(root)
	flush()

	for _, node := range order {
		splitText(node, insertions[node])
	}
}

func splitText(node *html.Node, offsets map[int]bool) {
	parent := node.Parent
	if parent == nil {
		return
	}
	text := node.Data
	start := 0
	for i := 1; i < len(text); i++ {
		if !offsets[i] {
			continue
		}
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: text[start:i]}, node)
		parent.InsertBefore(&html.Node{Type: html.ElementNode, Data: "wbr"}, node)
		start = i
	}
	node.Data = text[start:]
}
