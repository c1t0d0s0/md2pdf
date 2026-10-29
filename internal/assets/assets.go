// Package assets embeds the bundled templates, fonts, and JavaScript
// libraries so md2pdf works offline as a single binary.
package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

//go:embed templates vendor
var files embed.FS

// FS returns the embedded asset tree. Paths are rooted at "templates/"
// and "vendor/".
func FS() fs.FS { return files }

// TemplateNames lists the bundled template presets in sorted order.
func TemplateNames() []string {
	entries, err := fs.ReadDir(files, "templates")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// IsTemplate reports whether name is a bundled template preset.
func IsTemplate(name string) bool {
	for _, n := range TemplateNames() {
		if n == name {
			return true
		}
	}
	return false
}

// Font describes one face of a bundled font.
type Font struct {
	Family string
	File   string // path within the embedded FS
	Weight string // CSS font-weight range
}

// Fonts are the bundled variable fonts (SIL Open Font License 1.1).
var Fonts = []Font{
	{Family: "Noto Sans JP", File: "vendor/fonts/NotoSansJP.ttf", Weight: "100 900"},
	{Family: "Noto Serif JP", File: "vendor/fonts/NotoSerifJP.ttf", Weight: "200 900"},
	{Family: "Noto Sans Mono", File: "vendor/fonts/NotoSansMono.ttf", Weight: "100 900"},
}

const (
	MathJax    = "vendor/mathjax/tex-svg-full.js"
	Mermaid    = "vendor/mermaid/mermaid.min.js"
	Highlight  = "vendor/hljs/github.css"
	TemplateFS = "templates"
)

// Extract copies the embedded subtree rooted at src into dst.
func Extract(src, dst string) error {
	return fs.WalkDir(files, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		return os.WriteFile(target, data, 0o600)
	})
}
