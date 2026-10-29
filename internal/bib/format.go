package bib

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// Name is a parsed personal or organization name.
type Name struct {
	Family  string
	Given   string
	Literal string // organization name written as {{...}}
}

// splitTopLevel splits s on sep at brace depth 0.
func splitTopLevel(s, sep string) []string {
	var parts []string
	depth, start := 0, 0
	lower := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 && strings.HasPrefix(lower[i:], sep) {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(parts, s[start:])
}

// ParseNames parses a BibTeX name list ("A and B and others").
func ParseNames(raw string) (names []Name, others bool) {
	raw = strings.Join(strings.Fields(raw), " ")
	for _, part := range splitTopLevel(raw, " and ") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
			continue
		case part == "others":
			others = true
			continue
		case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && balancedOuter(part):
			names = append(names, Name{Literal: decodeLaTeX(part)})
			continue
		}
		if comma := splitTopLevel(part, ","); len(comma) >= 2 {
			// "von Last, Jr, First" or "von Last, First"
			given := comma[len(comma)-1]
			names = append(names, Name{Family: decodeLaTeX(comma[0]), Given: decodeLaTeX(given)})
			continue
		}
		words := splitTopLevel(part, " ")
		if len(words) == 1 {
			names = append(names, Name{Family: decodeLaTeX(words[0])})
			continue
		}
		// "First von Last": the last name starts at the first lowercase word
		// after the first name, or is the final word if there is none.
		split := len(words) - 1
		for i := 1; i < len(words)-1; i++ {
			if w := strings.TrimLeft(words[i], "{"); w != "" && unicode.IsLower(rune(w[0])) {
				split = i
				break
			}
		}
		names = append(names, Name{
			Given:  decodeLaTeX(strings.Join(words[:split], " ")),
			Family: decodeLaTeX(strings.Join(words[split:], " ")),
		})
	}
	return names, others
}

func balancedOuter(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 && i != len(s)-1 {
				return false
			}
		}
	}
	return depth == 0
}

// initials converts "Lars G." to "LG" and "Jean-Pierre" to "JP".
func initials(given string) string {
	var b strings.Builder
	for _, word := range strings.FieldsFunc(given, func(r rune) bool { return r == ' ' || r == '.' || r == '-' }) {
		r := []rune(word)
		if len(r) > 0 && unicode.IsLetter(r[0]) {
			b.WriteRune(unicode.ToUpper(r[0]))
		}
	}
	return b.String()
}

func formatName(n Name) string {
	if n.Literal != "" {
		return n.Literal
	}
	if n.Given == "" {
		return n.Family
	}
	return n.Family + " " + initials(n.Given)
}

func formatNames(raw string) string {
	names, others := ParseNames(raw)
	if len(names) == 0 {
		return ""
	}
	const limit = 6
	var parts []string
	for i, n := range names {
		if i == limit {
			others = true
			break
		}
		parts = append(parts, formatName(n))
	}
	s := strings.Join(parts, ", ")
	if others {
		s += ", et al"
	}
	return s
}

// builder accumulates the entry being formatted as DOM nodes.
type builder struct{ nodes []*html.Node }

func (b *builder) text(s string) {
	if s == "" {
		return
	}
	if n := len(b.nodes); n > 0 && b.nodes[n-1].Type == html.TextNode {
		b.nodes[n-1].Data += s
		return
	}
	b.nodes = append(b.nodes, &html.Node{Type: html.TextNode, Data: s})
}

func (b *builder) link(href, label string) {
	a := &html.Node{Type: html.ElementNode, Data: "a", Attr: []html.Attribute{{Key: "href", Val: href}}}
	a.AppendChild(&html.Node{Type: html.TextNode, Data: label})
	b.nodes = append(b.nodes, a)
}

// sentence appends s, adding a period unless it already ends with
// terminal punctuation.
func (b *builder) sentence(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}
	b.text(s + " ")
}

// Format renders the entry in a Vancouver-like style.
func Format(e Entry) []*html.Node {
	f := e.Fields
	b := &builder{}
	if a := formatNames(e.rawNames["author"]); a != "" {
		b.sentence(a)
	} else if ed := formatNames(e.rawNames["editor"]); ed != "" {
		b.sentence(ed + ", editors")
	}
	online := f["url"] != "" && (e.Type == "misc" || e.Type == "online" || e.Type == "electronic" || e.Type == "www")
	title := f["title"]
	switch e.Type {
	case "phdthesis":
		title += " [dissertation]"
	case "mastersthesis":
		title += " [master's thesis]"
	}
	if online {
		title += " [Internet]"
	}
	if e.Type == "inproceedings" || e.Type == "incollection" || e.Type == "conference" || e.Type == "inbook" && f["booktitle"] != "" {
		b.sentence(title)
		in := "In: "
		if ed := formatNames(e.rawNames["editor"]); ed != "" {
			in += ed + ", editors. "
		}
		b.sentence(in + f["booktitle"])
	} else {
		b.sentence(title)
	}
	year := strings.TrimSpace(f["year"])
	if year == "" {
		year = strings.TrimSpace(f["date"])
	}
	if m := f["month"]; m != "" && year != "" && e.Type != "article" {
		year += " " + m
	}

	publisher := f["publisher"]
	switch e.Type {
	case "techreport":
		publisher = f["institution"]
	case "phdthesis", "mastersthesis":
		publisher = f["school"]
	}
	place := f["address"]
	if place == "" {
		place = f["location"]
	}

	switch e.Type {
	case "article":
		if j := f["journal"]; j != "" {
			b.sentence(j)
		}
		s := year
		if m := f["month"]; m != "" && s != "" {
			s += " " + m
		}
		if v := f["volume"]; v != "" {
			s += ";" + v
			if n := f["number"]; n != "" {
				s += "(" + n + ")"
			}
		}
		if p := f["pages"]; p != "" {
			s += ":" + p
		}
		b.sentence(s)
	default:
		if ed := f["edition"]; ed != "" {
			b.sentence(ed + " ed")
		}
		pub := ""
		switch {
		case place != "" && publisher != "":
			pub = place + ": " + publisher
		case publisher != "":
			pub = publisher
		case place != "":
			pub = place
		}
		switch {
		case pub != "" && year != "":
			b.sentence(pub + "; " + year)
		case pub != "":
			b.sentence(pub)
		case year != "":
			b.sentence(year)
		}
		if p := f["pages"]; p != "" && e.Type != "book" {
			b.sentence("p. " + p)
		}
		if e.Type == "techreport" && f["number"] != "" {
			b.sentence("Report No.: " + f["number"])
		}
		if n := f["note"]; n != "" {
			b.sentence(n)
		}
	}
	if u := f["url"]; u != "" {
		b.text("Available from: ")
		b.link(u, u)
		b.text(" ")
	}
	if d := f["doi"]; d != "" {
		b.text("doi:")
		b.link("https://doi.org/"+d, d)
	}
	if n := len(b.nodes); n > 0 && b.nodes[n-1].Type == html.TextNode {
		b.nodes[n-1].Data = strings.TrimRight(b.nodes[n-1].Data, " ")
	}
	return b.nodes
}
