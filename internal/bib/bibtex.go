// Package bib parses BibTeX databases and formats numbered references
// in a Vancouver-like style.
package bib

import (
	"fmt"
	"strings"
	"unicode"
)

// Entry is one BibTeX record with LaTeX-decoded field values.
type Entry struct {
	Type   string // lowercase, e.g. "article"
	Key    string
	Fields map[string]string
	// Raw keeps the brace structure of name lists for author parsing.
	rawNames map[string]string
}

var monthMacros = map[string]string{
	"jan": "Jan", "feb": "Feb", "mar": "Mar", "apr": "Apr", "may": "May", "jun": "Jun",
	"jul": "Jul", "aug": "Aug", "sep": "Sep", "oct": "Oct", "nov": "Nov", "dec": "Dec",
}

type parser struct {
	src     []rune
	pos     int
	line    int
	source  string
	strings map[string]string
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: invalid BibTeX: %s", p.source, p.line, fmt.Sprintf(format, args...))
}

func (p *parser) peek() rune {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) next() rune {
	r := p.peek()
	p.pos++
	if r == '\n' {
		p.line++
	}
	return r
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.next()
	}
}

func (p *parser) ident() string {
	start := p.pos
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		if unicode.IsSpace(r) || strings.ContainsRune(`{}(),=#"`, r) {
			break
		}
		p.next()
	}
	return string(p.src[start:p.pos])
}

// Parse reads all entries of a BibTeX database. @string macros apply to
// subsequent entries; @comment and @preamble are ignored.
func Parse(source, content string, macros map[string]string) ([]Entry, error) {
	p := &parser{src: []rune(content), line: 1, source: source, strings: macros}
	var entries []Entry
	for {
		// Text outside entries is treated as a comment.
		for p.pos < len(p.src) && p.peek() != '@' {
			p.next()
		}
		if p.pos >= len(p.src) {
			return entries, nil
		}
		p.next() // '@'
		typ := strings.ToLower(strings.TrimSpace(p.ident()))
		p.skipSpace()
		open := p.next()
		if open != '{' && open != '(' {
			return nil, p.errorf("expected '{' after @%s", typ)
		}
		closer := '}'
		if open == '(' {
			closer = ')'
		}
		switch typ {
		case "comment", "preamble":
			if err := p.skipBalanced(closer); err != nil {
				return nil, err
			}
			continue
		case "string":
			p.skipSpace()
			name := strings.ToLower(p.ident())
			p.skipSpace()
			if p.next() != '=' {
				return nil, p.errorf("expected '=' in @string")
			}
			value, _, err := p.value()
			if err != nil {
				return nil, err
			}
			p.strings[name] = value
			p.skipSpace()
			if p.next() != closer {
				return nil, p.errorf("unclosed @string")
			}
			continue
		}
		p.skipSpace()
		key := strings.TrimSpace(p.ident())
		if key == "" {
			return nil, p.errorf("entry has no citation key")
		}
		e := Entry{Type: typ, Key: key, Fields: map[string]string{}, rawNames: map[string]string{}}
		for {
			p.skipSpace()
			r := p.next()
			if r == closer {
				break
			}
			if r != ',' {
				return nil, p.errorf("expected ',' or end of entry %s", key)
			}
			p.skipSpace()
			if p.peek() == closer {
				p.next()
				break
			}
			field := strings.ToLower(p.ident())
			if field == "" {
				return nil, p.errorf("expected a field name in entry %s", key)
			}
			p.skipSpace()
			if p.next() != '=' {
				return nil, p.errorf("expected '=' after field %s in entry %s", field, key)
			}
			value, raw, err := p.value()
			if err != nil {
				return nil, err
			}
			e.Fields[field] = value
			e.rawNames[field] = raw
		}
		entries = append(entries, e)
	}
}

func (p *parser) skipBalanced(closer rune) error {
	depth := 0
	for p.pos < len(p.src) {
		r := p.next()
		switch {
		case r == '{':
			depth++
		case r == '}' && depth > 0:
			depth--
		case r == closer && depth == 0:
			return nil
		}
	}
	return p.errorf("unexpected end of input")
}

// value parses `{...}`, `"..."`, numbers, and macros joined by '#', and
// returns the decoded text and the raw text with braces kept.
func (p *parser) value() (string, string, error) {
	var raw strings.Builder
	for {
		p.skipSpace()
		switch r := p.peek(); {
		case r == '{':
			p.next()
			s, err := p.delimited('}')
			if err != nil {
				return "", "", err
			}
			raw.WriteString(s)
		case r == '"':
			p.next()
			s, err := p.delimited('"')
			if err != nil {
				return "", "", err
			}
			raw.WriteString(s)
		default:
			name := p.ident()
			if name == "" {
				return "", "", p.errorf("expected a field value")
			}
			if v, ok := p.strings[strings.ToLower(name)]; ok {
				raw.WriteString(v)
			} else if m, ok := monthMacros[strings.ToLower(name)]; ok {
				raw.WriteString(m)
			} else if strings.Trim(name, "0123456789") == "" {
				raw.WriteString(name)
			} else {
				return "", "", p.errorf("undefined string macro %q", name)
			}
		}
		p.skipSpace()
		if p.peek() != '#' {
			break
		}
		p.next()
	}
	return decodeLaTeX(raw.String()), raw.String(), nil
}

func (p *parser) delimited(end rune) (string, error) {
	start := p.pos
	depth := 0
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		switch {
		case r == '\\':
			p.next()
		case r == '{':
			depth++
		case r == '}' && depth > 0:
			depth--
		case r == end && depth == 0:
			s := string(p.src[start:p.pos])
			p.next()
			return s, nil
		}
		p.next()
	}
	return "", p.errorf("unterminated field value")
}

var accents = map[byte]map[rune]rune{
	'\'': {'a': 'á', 'e': 'é', 'i': 'í', 'o': 'ó', 'u': 'ú', 'y': 'ý', 'c': 'ć', 'n': 'ń', 's': 'ś', 'z': 'ź',
		'A': 'Á', 'E': 'É', 'I': 'Í', 'O': 'Ó', 'U': 'Ú', 'Y': 'Ý', 'C': 'Ć', 'N': 'Ń', 'S': 'Ś', 'Z': 'Ź'},
	'`': {'a': 'à', 'e': 'è', 'i': 'ì', 'o': 'ò', 'u': 'ù', 'A': 'À', 'E': 'È', 'I': 'Ì', 'O': 'Ò', 'U': 'Ù'},
	'^': {'a': 'â', 'e': 'ê', 'i': 'î', 'o': 'ô', 'u': 'û', 'A': 'Â', 'E': 'Ê', 'I': 'Î', 'O': 'Ô', 'U': 'Û'},
	'"': {'a': 'ä', 'e': 'ë', 'i': 'ï', 'o': 'ö', 'u': 'ü', 'y': 'ÿ', 'A': 'Ä', 'E': 'Ë', 'I': 'Ï', 'O': 'Ö', 'U': 'Ü'},
	'~': {'a': 'ã', 'n': 'ñ', 'o': 'õ', 'A': 'Ã', 'N': 'Ñ', 'O': 'Õ'},
	'c': {'c': 'ç', 'C': 'Ç', 's': 'ş', 'S': 'Ş'},
	'v': {'c': 'č', 's': 'š', 'z': 'ž', 'r': 'ř', 'e': 'ě', 'C': 'Č', 'S': 'Š', 'Z': 'Ž', 'R': 'Ř', 'E': 'Ě'},
	'=': {'a': 'ā', 'e': 'ē', 'i': 'ī', 'o': 'ō', 'u': 'ū'},
}

var commands = map[string]string{
	"ss": "ß", "ae": "æ", "AE": "Æ", "oe": "œ", "OE": "Œ", "o": "ø", "O": "Ø",
	"aa": "å", "AA": "Å", "l": "ł", "L": "Ł", "i": "ı", "textendash": "–", "textemdash": "—",
	"LaTeX": "LaTeX", "TeX": "TeX", "textregistered": "®", "copyright": "©", "dots": "…", "ldots": "…",
}

// decodeLaTeX converts common LaTeX markup to plain Unicode text.
func decodeLaTeX(s string) string {
	src := []rune(s)
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		r := src[i]
		switch r {
		case '{', '}':
			continue
		case '~':
			b.WriteRune(' ')
			continue
		case '-':
			if i+2 < len(src) && src[i+1] == '-' && src[i+2] == '-' {
				b.WriteRune('—')
				i += 2
				continue
			}
			if i+1 < len(src) && src[i+1] == '-' {
				b.WriteRune('–')
				i++
				continue
			}
		case '\\':
			if i+1 >= len(src) {
				continue
			}
			c := src[i+1]
			if strings.ContainsRune(`&%$#_{}~`, c) && c != '~' {
				b.WriteRune(c)
				i++
				continue
			}
			// Accents: \'e, \'{e}, {\'e}. Letter commands need a delimiter: \c{c}, \v s.
			letterCmd := c == 'c' || c == 'v'
			if m, ok := accents[byte(c)]; ok && c < 0x80 && (!letterCmd || (i+2 < len(src) && (src[i+2] == '{' || src[i+2] == ' '))) {
				j := i + 2
				for j < len(src) && (src[j] == '{' || src[j] == ' ') {
					j++
				}
				if j < len(src) {
					if out, ok := m[src[j]]; ok {
						b.WriteRune(out)
						i = j
						continue
					}
				}
			}
			j := i + 1
			for j < len(src) && unicode.IsLetter(src[j]) {
				j++
			}
			name := string(src[i+1 : j])
			if v, ok := commands[name]; ok {
				b.WriteString(v)
				i = j - 1
				if i+1 < len(src) && src[i+1] == ' ' {
					i++
				}
				continue
			}
			// Unknown command: drop the command, keep the argument text.
			if name != "" {
				i = j - 1
				continue
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
