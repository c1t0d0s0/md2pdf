package document

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/markdown"
)

var (
	commentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	importRe  = regexp.MustCompile(`@import\s+(?:url\(\s*)?(['"])([^'"]+)['"]\s*\)?\s*([^;]*);`)
	urlRe     = regexp.MustCompile(`url\(\s*(['"]?)([^'")]+?)['"]?\s*\)`)
)

// loadCSS reads a stylesheet, inlines relative @import rules, and
// rewrites relative url() references to absolute file:// URLs.
func loadCSS(path string, seen map[string]bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if seen[abs] {
		return "", errs.Input("stylesheet import cycle: %s", abs)
	}
	seen[abs] = true
	defer delete(seen, abs)

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", errs.Input("cannot read stylesheet %s: %v", abs, err)
	}
	css := commentRe.ReplaceAllString(string(data), "")
	dir := filepath.Dir(abs)

	var firstErr error
	css = importRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := importRe.FindStringSubmatch(m)
		target, media := sub[2], strings.TrimSpace(sub[3])
		if hasScheme(target) {
			return m
		}
		inner, err := loadCSS(filepath.Join(dir, filepath.FromSlash(target)), seen)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return ""
		}
		if media != "" {
			return "@media " + media + " {\n" + inner + "\n}\n"
		}
		return inner
	})
	if firstErr != nil {
		return "", firstErr
	}
	css = urlRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := urlRe.FindStringSubmatch(m)
		target := sub[2]
		if hasScheme(target) || strings.HasPrefix(target, "#") {
			return m
		}
		resolved, err := markdown.ResolveLocal(target, dir, abs)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		return "url(" + strconv.Quote(resolved) + ")"
	})
	return css, firstErr
}

var schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

func hasScheme(u string) bool {
	return strings.HasPrefix(u, "//") || schemeRe.MatchString(u)
}

// cssString quotes s as a CSS string literal.
func cssString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\` + strconv.FormatInt(int64(r), 16) + " ")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Vivliostyle-only features used by pfpdf-style templates, and their Chromium
// replacements. Named strings are evaluated statically because the values
// they hold (title, series, confidential) are always document constants.
var (
	stringSetRuleRe = regexp.MustCompile(`([^{}]+)\{([^{}]*\bstring-set\s*:[^{}]*)\}`)
	stringSetDeclRe = regexp.MustCompile(`\bstring-set\s*:\s*([A-Za-z0-9_-]+)\s+([^;}]+)`)
	stringFnRe      = regexp.MustCompile(`\bstring\(\s*([A-Za-z0-9_-]+)\s*(?:,\s*[a-z-]+\s*)?\)`)
	envRe           = regexp.MustCompile(`\benv\(\s*(pub-title|doc-title)\s*\)`)
	elementFnRe     = regexp.MustCompile(`\belement\(\s*([A-Za-z0-9_-]+)\s*\)`)
	runningRe       = regexp.MustCompile(`\bposition\s*:\s*running\([^)]*\)`)
	leaderCounterRe = regexp.MustCompile(`\bleader\((?:"[^"]*"|'[^']*'|[a-z-]+)\)\s*`)
	targetCounterRe = regexp.MustCompile(`\btarget-counter\(\s*attr\(\s*href(?:\s+url)?\s*\)\s*,\s*page\s*(?:,\s*[A-Za-z0-9_-]+\s*)?\)`)
	stringTokenRe   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|\bcontent\(\s*(?:text)?\s*\)`)
	classRe         = regexp.MustCompile(`\.([A-Za-z0-9_-]+)`)
)

// cssContext supplies document values for the Vivliostyle translation.
type cssContext struct {
	title string
	// slotText returns the text of the slot element having any of the given
	// classes, and whether that slot exists in the final document.
	slotText func(classes []string) (string, bool)
}

// translateResult reports the substitutions a stylesheet needed.
type translateResult struct {
	css       string
	usesPages bool // replaced target-counter() with TOC page attributes
	leader    bool // removed leader(); md2pdf supplies leader elements
}

func translateCSS(css string, ctx cssContext, named map[string]string) translateResult {
	res := translateResult{}
	css = envRe.ReplaceAllString(css, cssString(ctx.title))
	css = stringFnRe.ReplaceAllStringFunc(css, func(m string) string {
		name := stringFnRe.FindStringSubmatch(m)[1]
		return cssString(named[name])
	})
	css = elementFnRe.ReplaceAllStringFunc(css, func(m string) string {
		name := elementFnRe.FindStringSubmatch(m)[1]
		if strings.Contains(name, "confidential") {
			return cssString(named["pfpdf-confidential"])
		}
		return `""`
	})
	css = runningRe.ReplaceAllString(css, "display: none")
	if leaderCounterRe.MatchString(css) {
		res.leader = true
		css = leaderCounterRe.ReplaceAllString(css, "")
	}
	if targetCounterRe.MatchString(css) {
		res.usesPages = true
		css = targetCounterRe.ReplaceAllString(css, "attr(data-md2pdf-page)")
	}
	res.css = css
	return res
}

// collectNamedStrings evaluates each `string-set` declaration against the
// slot elements its rule selects.
func collectNamedStrings(sheets []string, ctx cssContext, named map[string]string) {
	for _, css := range sheets {
		for _, rule := range stringSetRuleRe.FindAllStringSubmatch(css, -1) {
			selector, body := rule[1], rule[2]
			var classes []string
			for _, c := range classRe.FindAllStringSubmatch(selector, -1) {
				classes = append(classes, c[1])
			}
			text, ok := ctx.slotText(classes)
			for _, decl := range stringSetDeclRe.FindAllStringSubmatch(body, -1) {
				name, expr := decl[1], decl[2]
				if !ok {
					if _, exists := named[name]; !exists {
						named[name] = ""
					}
					continue
				}
				var b strings.Builder
				for _, tok := range stringTokenRe.FindAllStringSubmatch(expr, -1) {
					switch {
					case strings.HasPrefix(tok[0], "content"):
						b.WriteString(text)
					case tok[0][0] == '"':
						b.WriteString(unescapeCSS(tok[1]))
					default:
						b.WriteString(unescapeCSS(tok[2]))
					}
				}
				named[name] = b.String()
			}
		}
	}
}

func unescapeCSS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
