// Package input discovers Markdown sources, validates UTF-8, and resolves
// front matter into document metadata.
package input

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"

	"github.com/c1t0d0s0/md2pdf/internal/errs"
)

// SourceFile is one Markdown input with its front matter stripped.
type SourceFile struct {
	Path       string // absolute path
	Name       string // base name, used for cross-file links
	Content    string
	LineOffset int // lines taken by the stripped front matter block
}

// Title is a validated document title.
type Title struct {
	Lines     []string // display lines split at <br>
	PlainText string   // lines joined with "\n"
}

// Metadata is document-level information from front matter.
type Metadata struct {
	Title        Title
	Author       string
	Series       string
	Date         string
	Confidential bool
	PageSize     string // CSS size value, e.g. "210mm 297mm"
	Lang         string
	Dir          string // ltr / rtl / auto
}

// Settings are document options the CLI can override.
type Settings struct {
	Template *string
	TOC      *bool
	Logo     *Logo
}

// Logo is the front matter logo setting.
type Logo struct {
	None bool
	Path string // absolute path unless !None
}

// BibliographyFile is a BibTeX database declared in front matter.
type BibliographyFile struct {
	Path    string // absolute path
	Content string
}

// Document is the fully resolved input.
type Document struct {
	Files        []SourceFile
	Metadata     Metadata
	Settings     Settings
	Bibliography []BibliographyFile
}

var allowedKeys = map[string]bool{
	"title": true, "author": true, "series": true, "date": true, "page_size": true,
	"confidential": true, "lang": true, "dir": true, "template": true, "toc": true, "logo": true,
	"bibliography": true,
}

var pageSizeKeywords = map[string]string{
	"a3":     "297mm 420mm",
	"a4":     "210mm 297mm",
	"a5":     "148mm 210mm",
	"jis-b4": "257mm 364mm",
	"jis-b5": "182mm 257mm",
	"iso-b4": "250mm 353mm",
	"iso-b5": "176mm 250mm",
	"letter": "8.5in 11in",
	"legal":  "8.5in 14in",
}

var pageSizeRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(mm|cm|in|pt)[ \t\n\f\r]+([0-9]+(?:\.[0-9]+)?)(mm|cm|in|pt)$`)

// Options controls how inputs are resolved.
type Options struct {
	CLITitle    *string
	IsTemplate  func(string) bool
	Warn        func(string)
	Now         time.Time
	SourceEpoch *int64
}

// ResolvePaths returns the Markdown files for a file or directory input.
func ResolvePaths(inputAbs string) ([]string, error) {
	st, err := os.Stat(inputAbs)
	if err != nil {
		return nil, errs.Input("input not found: %s", inputAbs)
	}
	if st.Mode().IsRegular() {
		if !strings.HasSuffix(inputAbs, ".md") {
			return nil, errs.Input("input file must have a lowercase .md extension: %s", inputAbs)
		}
		return []string{inputAbs}, nil
	}
	if !st.IsDir() {
		return nil, errs.Input("input is neither a regular file nor a directory: %s", inputAbs)
	}
	entries, err := os.ReadDir(inputAbs)
	if err != nil {
		return nil, errs.Input("cannot read input directory %s: %v", inputAbs, err)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !utf8.ValidString(name) {
			return nil, errs.Input("input directory contains a file name that is not valid UTF-8")
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := os.Stat(filepath.Join(inputAbs, name))
		if err != nil {
			return nil, errs.Input("cannot stat %s: %v", filepath.Join(inputAbs, name), err)
		}
		if info.Mode().IsRegular() {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, errs.Input("no *.md files found in %s", inputAbs)
	}
	// Locale-independent bytewise sort.
	sort.Strings(names)
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(inputAbs, n)
	}
	return paths, nil
}

func readUTF8(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errs.Input("cannot read input file %s: %v", path, err)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return "", errs.Input("%s: invalid UTF-8 byte sequence", path)
	}
	return string(data), nil
}

type frontMatter struct {
	raw       map[string]any
	lineCount int
}

var lineSplitRe = regexp.MustCompile(`\r\n|\n`)

func parseFrontMatter(path, text string) (*frontMatter, string, error) {
	lines := lineSplitRe.Split(text, -1)
	if lines[0] != "---" {
		return nil, text, nil
	}
	closed := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" || lines[i] == "..." {
			closed = i
			break
		}
	}
	if closed < 0 {
		return nil, "", errs.Input("%s: front matter is not closed with --- or ...", path)
	}
	yamlText := strings.Join(lines[1:closed], "\n")
	raw := map[string]any{}
	if strings.TrimSpace(yamlText) != "" {
		var parsed any
		if err := yaml.Unmarshal([]byte(yamlText), &parsed); err != nil {
			return nil, "", errs.Input("%s: invalid front matter: %v", path, err)
		}
		m, ok := parsed.(map[string]any)
		if !ok {
			return nil, "", errs.Input("%s: front matter must be a mapping", path)
		}
		raw = m
	}
	for key := range raw {
		if !allowedKeys[key] {
			return nil, "", errs.Input("%s: unknown front matter key: %s", path, key)
		}
	}
	// Re-locate the body by byte offset to preserve CRLF input.
	offset := 0
	for i := 0; i <= closed; i++ {
		offset += len(lines[i])
		if offset < len(text) && text[offset] == '\r' {
			offset++
		}
		if offset < len(text) && text[offset] == '\n' {
			offset++
		}
	}
	return &frontMatter{raw: raw, lineCount: closed + 1}, text[offset:], nil
}

// ReadSettings reads only the document settings of the first input file.
func ReadSettings(inputAbs string, isTemplate func(string) bool) (Settings, error) {
	paths, err := ResolvePaths(inputAbs)
	if err != nil {
		return Settings{}, err
	}
	text, err := readUTF8(paths[0])
	if err != nil {
		return Settings{}, err
	}
	fm, _, err := parseFrontMatter(paths[0], text)
	if err != nil {
		return Settings{}, err
	}
	raw := map[string]any{}
	if fm != nil {
		raw = fm.raw
	}
	return resolveSettings(raw, paths[0], isTemplate)
}

func resolveSettings(raw map[string]any, fmPath string, isTemplate func(string) bool) (Settings, error) {
	var s Settings
	if v, ok := raw["template"]; ok {
		name, ok := v.(string)
		if !ok {
			return s, errs.Input("front matter template: expected a string")
		}
		if !isTemplate(name) {
			return s, errs.Input("front matter template: unknown bundled template: %s", name)
		}
		s.Template = &name
	}
	if v, ok := raw["toc"]; ok {
		b, ok := v.(bool)
		if !ok {
			return s, errs.Input("front matter toc: expected a YAML boolean")
		}
		s.TOC = &b
	}
	if v, ok := raw["logo"]; ok {
		switch logo := v.(type) {
		case bool:
			if logo {
				return s, errs.Input("front matter logo: expected a path string or false")
			}
			s.Logo = &Logo{None: true}
		case string:
			if logo == "" || strings.ContainsRune(logo, 0) {
				return s, errs.Input("front matter logo: empty paths and NUL are not allowed")
			}
			p := logo
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(fmPath), p)
			}
			s.Logo = &Logo{Path: filepath.Clean(p)}
		default:
			return s, errs.Input("front matter logo: expected a path string or false")
		}
	}
	return s, nil
}

// ParsePageSize converts a page_size value into a CSS size.
func ParsePageSize(value string) (string, error) {
	if kw, ok := pageSizeKeywords[strings.ToLower(value)]; ok {
		return kw, nil
	}
	m := pageSizeRe.FindStringSubmatch(value)
	if m == nil {
		return "", errs.Input("page_size: invalid value %q", value)
	}
	w, _ := strconv.ParseFloat(m[1], 64)
	h, _ := strconv.ParseFloat(m[3], 64)
	if w <= 0 || h <= 0 {
		return "", errs.Input("page_size: each side must be positive")
	}
	return m[1] + m[2] + " " + m[3] + m[4], nil
}

var (
	tagRe     = regexp.MustCompile(`<[^>]*>?`)
	brRe      = regexp.MustCompile(`(?i)^<br\s*/?>$`)
	tagLikeRe = regexp.MustCompile(`^<[a-zA-Z/!]`)
	brSplitRe = regexp.MustCompile(`(?i)<br\s*/?>`)
)

// ValidateTitle accepts only text, character references, and bare <br> tags.
func ValidateTitle(title string) error {
	if title == "" {
		return errs.Input("title must be a non-empty string")
	}
	if strings.ContainsAny(title, "\r\n") {
		return errs.Input("title: line breaks are not allowed; use <br> instead")
	}
	for _, tag := range tagRe.FindAllString(title, -1) {
		if brRe.MatchString(tag) || !tagLikeRe.MatchString(tag) {
			continue
		}
		return errs.Input("title: HTML other than <br> is not allowed: %s", tag)
	}
	return nil
}

// NormalizeTitle validates a title and splits it into display lines.
func NormalizeTitle(title string) (Title, error) {
	if err := ValidateTitle(title); err != nil {
		return Title{}, err
	}
	parts := brSplitRe.Split(title, -1)
	lines := make([]string, len(parts))
	for i, p := range parts {
		lines[i] = html.UnescapeString(p)
	}
	return Title{Lines: lines, PlainText: strings.Join(lines, "\n")}, nil
}

func isJapanese(lang string) bool {
	return lang == "ja" || strings.HasPrefix(lang, "ja-")
}

func formatDisplayDate(lang string, t time.Time) string {
	if isJapanese(lang) {
		return fmt.Sprintf("%d 年 %d 月 %d 日", t.Year(), int(t.Month()), t.Day())
	}
	return t.Format("2006-01-02")
}

// ParseSourceDateEpoch validates SOURCE_DATE_EPOCH. nil means unset.
func ParseSourceDateEpoch(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strings.HasPrefix(value, "+") || len(value) > 13 {
		return nil, errs.Input("SOURCE_DATE_EPOCH: expected a non-negative decimal integer, got %q", value)
	}
	return &n, nil
}

// Resolve reads all input files and resolves document metadata.
func Resolve(inputAbs string, opts Options) (*Document, error) {
	paths, err := ResolvePaths(inputAbs)
	if err != nil {
		return nil, err
	}
	doc := &Document{}
	var fm *frontMatter
	for i, p := range paths {
		text, err := readUTF8(p)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			parsed, body, err := parseFrontMatter(p, text)
			if err != nil {
				return nil, err
			}
			fm = parsed
			offset := 0
			if fm != nil {
				offset = fm.lineCount
			}
			doc.Files = append(doc.Files, SourceFile{Path: p, Name: filepath.Base(p), Content: body, LineOffset: offset})
			continue
		}
		if lineSplitRe.Split(text, 2)[0] == "---" {
			return nil, errs.Input("%s: duplicate front matter; only the first file may have one", p)
		}
		doc.Files = append(doc.Files, SourceFile{Path: p, Name: filepath.Base(p), Content: text})
	}

	raw := map[string]any{}
	if fm != nil {
		raw = fm.raw
	}
	if doc.Settings, err = resolveSettings(raw, paths[0], opts.IsTemplate); err != nil {
		return nil, err
	}
	if doc.Bibliography, err = resolveBibliography(raw["bibliography"], paths[0]); err != nil {
		return nil, err
	}

	str := func(key string) (string, bool, error) {
		v, ok := raw[key]
		if !ok {
			return "", false, nil
		}
		switch s := v.(type) {
		case string:
			return s, true, nil
		case time.Time:
			// A YAML timestamp such as `date: 2025-01-31`.
			return s.Format("2006-01-02"), true, nil
		}
		return "", false, errs.Input("front matter %s: expected a string", key)
	}

	md := &doc.Metadata
	fmTitle, hasTitle, err := str("title")
	if err != nil {
		return nil, err
	}
	rawTitle := fmTitle
	if opts.CLITitle != nil {
		if hasTitle {
			// Validate an overridden front matter title too.
			if _, err := NormalizeTitle(fmTitle); err != nil {
				return nil, err
			}
		}
		rawTitle, hasTitle = *opts.CLITitle, true
	}
	if !hasTitle {
		return nil, errs.Input("title is required: set it in front matter or with --title")
	}
	if md.Title, err = NormalizeTitle(rawTitle); err != nil {
		return nil, err
	}
	if md.Author, _, err = str("author"); err != nil {
		return nil, err
	}
	if md.Series, _, err = str("series"); err != nil {
		return nil, err
	}
	explicitDate, hasDate, err := str("date")
	if err != nil {
		return nil, err
	}
	if v, ok := raw["confidential"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, errs.Input("front matter confidential: expected a YAML boolean")
		}
		md.Confidential = b
	}
	pageSize, hasPageSize, err := str("page_size")
	if err != nil {
		return nil, err
	}
	if !hasPageSize {
		pageSize = "A4"
	}
	if md.PageSize, err = ParsePageSize(pageSize); err != nil {
		return nil, err
	}
	lang, hasLang, err := str("lang")
	if err != nil {
		return nil, err
	}
	if !hasLang {
		lang = "ja"
	}
	tag, err := language.Parse(lang)
	if err != nil {
		return nil, errs.Input("front matter lang: not a well-formed BCP 47 language tag: %s", lang)
	}
	md.Lang = tag.String()
	dir, hasDir, err := str("dir")
	if err != nil {
		return nil, err
	}
	if !hasDir {
		dir = "auto"
	}
	if dir != "ltr" && dir != "rtl" && dir != "auto" {
		return nil, errs.Input("front matter dir: expected ltr / rtl / auto, got %s", dir)
	}
	md.Dir = dir

	switch {
	case hasDate:
		md.Date = explicitDate
	case opts.SourceEpoch != nil:
		md.Date = formatDisplayDate(md.Lang, time.Unix(*opts.SourceEpoch, 0).UTC())
	default:
		if opts.Warn != nil {
			opts.Warn("SOURCE_DATE_EPOCH is not set; this build is not reproducible")
		}
		md.Date = formatDisplayDate(md.Lang, opts.Now)
	}
	return doc, nil
}

func resolveBibliography(value any, fmPath string) ([]BibliographyFile, error) {
	if value == nil {
		return nil, nil
	}
	var declared []string
	switch v := value.(type) {
	case string:
		declared = []string{v}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				declared = nil
				break
			}
			declared = append(declared, s)
		}
	}
	if len(declared) == 0 {
		return nil, errs.Input("front matter bibliography: expected a string or non-empty string list")
	}
	var files []BibliographyFile
	seen := map[string]bool{}
	for _, d := range declared {
		if d == "" || strings.ContainsRune(d, 0) {
			return nil, errs.Input("front matter bibliography: empty paths and NUL are not allowed")
		}
		if !strings.HasSuffix(strings.ToLower(d), ".bib") {
			return nil, errs.Input("front matter bibliography: expected a .bib file: %s", d)
		}
		p := d
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(fmPath), p)
		}
		p = filepath.Clean(p)
		if seen[p] {
			return nil, errs.Input("front matter bibliography: duplicate path: %s", d)
		}
		seen[p] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, errs.Input("cannot read bibliography %s: %v", p, err)
		}
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(data) {
			return nil, errs.Input("%s: invalid UTF-8 byte sequence", p)
		}
		files = append(files, BibliographyFile{Path: p, Content: string(data)})
	}
	return files, nil
}
