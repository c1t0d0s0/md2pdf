package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1t0d0s0/md2pdf/internal/pdfpost"
	"github.com/c1t0d0s0/md2pdf/internal/render"
)

type testLogger struct{ t *testing.T }

func (l testLogger) Warn(m string)  { l.t.Log("warn: " + m) }
func (l testLogger) Info(m string)  { l.t.Log("info: " + m) }
func (l testLogger) Debug(m string) {}

const doc = `---
title: E2E テスト
author: Tester
bibliography: refs.bib
---

# はじめに

これは**「重要」**です。数式 $E = mc^2$ と引用\cite{k1}。

___

# 図

` + "```mermaid\nflowchart LR\n  A --> B\n```" + `

$$
\int_0^1 x\,dx = \frac{1}{2}
$$

\printbibliography
`

// TestEndToEnd renders a document that exercises every browser-side feature.
// It requires a Chromium-based browser. Set MD2PDF_TEST_NO_SANDBOX=1 where
// the sandbox cannot start.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := render.FindBrowser(""); err != nil {
		t.Skip("no browser: ", err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1767225600")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.md"), []byte(doc), 0o644)
	os.WriteFile(filepath.Join(dir, "refs.bib"), []byte(`@book{k1, author={Knuth, Donald E.}, title={The TeXbook}, publisher={Addison-Wesley}, year=1984}`), 0o644)
	out := filepath.Join(dir, "out", "doc.pdf")
	cfg := Config{
		Input:     filepath.Join(dir, "doc.md"),
		Output:    out,
		Timeout:   2 * time.Minute,
		NoSandbox: os.Getenv("MD2PDF_TEST_NO_SANDBOX") != "",
		Log:       testLogger{t},
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// cover, TOC, chapter 1, chapter 2 (forced page break)
	if res.Pages != 4 {
		t.Errorf("pages = %d, want 4", res.Pages)
	}
	first, _ := os.ReadFile(out)
	pages, err := pdfpost.DestinationPages(first)
	if err != nil {
		t.Fatal(err)
	}
	if pages["はじめに"] != 3 || pages["図"] != 4 {
		t.Errorf("destinations: %v", pages)
	}
	for _, want := range []string{"/Lang(ja)", "/Producer(md2pdf)", "/CreationDate(D:20260101000000+00'00')"} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("output lacks %s", want)
		}
	}
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(out)
	if !bytes.Equal(first, second) {
		t.Error("output is not reproducible with SOURCE_DATE_EPOCH")
	}
}
