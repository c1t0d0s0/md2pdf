// Package build runs the end-to-end pipeline (input -> HTML -> browser ->
// final PDF).
package build

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1t0d0s0/md2pdf/internal/assets"
	"github.com/c1t0d0s0/md2pdf/internal/document"
	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/input"
	"github.com/c1t0d0s0/md2pdf/internal/markdown"
	"github.com/c1t0d0s0/md2pdf/internal/pdfpost"
	"github.com/c1t0d0s0/md2pdf/internal/render"
)

// Config is the resolved command-line configuration. Pointer fields are nil
// when the option was not given, so front matter can fill them in.
type Config struct {
	Input       string
	Output      string
	Title       *string
	TOC         *bool
	Template    *string
	Logo        *string
	NoLogo      bool
	FontDirs    []string
	BrowserPath string
	NoSandbox   bool
	Timeout     time.Duration
	KeepWorkDir bool
	Log         Logger
}

// Logger receives diagnostic messages.
type Logger interface {
	Warn(string)
	Info(string)
	Debug(string)
}

// Result describes the written PDF.
type Result struct {
	Output string
	Bytes  int
	Pages  int
}

// maxTOCPasses caps the re-renders used to settle TOC page numbers.
const maxTOCPasses = 4

// Run converts cfg.Input into cfg.Output.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	log := cfg.Log
	start := time.Now()
	epoch, err := input.ParseSourceDateEpoch(os.Getenv("SOURCE_DATE_EPOCH"))
	if err != nil {
		return nil, err
	}
	inputAbs, err := filepath.Abs(cfg.Input)
	if err != nil {
		return nil, errs.Input("--input: %v", err)
	}
	outputAbs, err := filepath.Abs(cfg.Output)
	if err != nil {
		return nil, errs.Input("--output: %v", err)
	}
	if err := validateOutput(outputAbs, inputAbs); err != nil {
		return nil, err
	}

	doc, err := input.Resolve(inputAbs, input.Options{
		CLITitle:    cfg.Title,
		IsTemplate:  assets.IsTemplate,
		Warn:        log.Debug,
		Now:         start,
		SourceEpoch: epoch,
	})
	if err != nil {
		return nil, err
	}

	// Precedence: built-in defaults < front matter < CLI.
	templateName := "default"
	if doc.Settings.Template != nil {
		templateName = *doc.Settings.Template
	}
	if cfg.Template != nil {
		templateName = *cfg.Template
	}
	toc := true
	if doc.Settings.TOC != nil {
		toc = *doc.Settings.TOC
	}
	if cfg.TOC != nil {
		toc = *cfg.TOC
	}
	var logo document.Logo
	if l := doc.Settings.Logo; l != nil {
		logo = document.Logo{None: l.None, Path: l.Path}
	}
	switch {
	case cfg.Logo != nil:
		p, err := filepath.Abs(*cfg.Logo)
		if err != nil {
			return nil, errs.Input("--logo: %v", err)
		}
		logo = document.Logo{Path: p}
	case cfg.NoLogo:
		logo = document.Logo{None: true}
	}
	log.Debug(fmt.Sprintf("template=%s toc=%v files=%d", templateName, toc, len(doc.Files)))

	workDir, err := os.MkdirTemp("", "md2pdf-")
	if err != nil {
		return nil, errs.Runtime("cannot create work directory: %v", err)
	}
	if cfg.KeepWorkDir {
		log.Info("keeping work directory: " + workDir)
	} else {
		defer os.RemoveAll(workDir)
	}

	tpl, err := document.LoadTemplate(templateName, workDir)
	if err != nil {
		return nil, err
	}
	body, err := markdown.Build(doc.Files, markdown.Options{
		ReservedIDs:  tpl.ReservedIDs,
		Bibliography: doc.Bibliography,
		Warn:         log.Warn,
	})
	if err != nil {
		return nil, err
	}
	assembled, err := document.Assemble(document.Options{
		Metadata: doc.Metadata,
		Body:     body,
		Template: tpl,
		Logo:     logo,
		TOC:      toc,
		FontDirs: cfg.FontDirs,
		WorkDir:  workDir,
		Warn:     log.Warn,
	})
	if err != nil {
		return nil, err
	}
	log.Debug("document: " + assembled.Path)

	session, err := render.Open(ctx, markdown.FileURL(assembled.Path), render.Options{
		BrowserPath: cfg.BrowserPath,
		Timeout:     cfg.Timeout,
		NoSandbox:   cfg.NoSandbox,
		Debugf:      func(f string, a ...any) { log.Debug(fmt.Sprintf(f, a...)) },
	})
	if err != nil {
		return nil, err
	}
	defer session.Close()

	pdf, err := session.PrintPDF()
	if err != nil {
		return nil, err
	}
	if assembled.TOCPages {
		// Chromium cannot resolve target-counter(), so read each heading's
		// page from the printed PDF, write the numbers into the TOC, and
		// print again until they stop moving.
		var previous map[string]int
		for pass := 1; ; pass++ {
			pages, err := pdfpost.DestinationPages(pdf)
			if err != nil {
				return nil, err
			}
			if maps.Equal(pages, previous) {
				break
			}
			if pass >= maxTOCPasses {
				log.Warn("table of contents page numbers did not converge")
				break
			}
			log.Debug(fmt.Sprintf("TOC pass %d: %d destinations", pass, len(pages)))
			if err := session.SetTOCPages(pages); err != nil {
				return nil, err
			}
			if pdf, err = session.PrintPDF(); err != nil {
				return nil, err
			}
			previous = pages
		}
	}
	session.Close()
	if cfg.KeepWorkDir {
		_ = os.WriteFile(filepath.Join(workDir, "renderer-output.pdf"), pdf, 0o600)
	}

	stamp := start
	if epoch != nil {
		stamp = time.Unix(*epoch, 0).UTC()
	}
	final, err := pdfpost.Finalize(pdf, pdfpost.Metadata{
		Title:  doc.Metadata.Title.PlainText,
		Author: doc.Metadata.Author,
		Lang:   doc.Metadata.Lang,
		Time:   stamp,
	})
	if err != nil {
		return nil, err
	}
	if err := commit(outputAbs, final); err != nil {
		return nil, err
	}
	pages, _ := pdfpost.PageCount(final)
	log.Info(fmt.Sprintf("wrote %s (%d bytes, %d pages, %.1fs)", outputAbs, len(final), pages, time.Since(start).Seconds()))
	return &Result{Output: outputAbs, Bytes: len(final), Pages: pages}, nil
}

func validateOutput(outputAbs, inputAbs string) error {
	if !strings.EqualFold(filepath.Ext(outputAbs), ".pdf") {
		return errs.Input("output must have a .pdf extension: %s", outputAbs)
	}
	if st, err := os.Lstat(outputAbs); err == nil && !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
		return errs.Input("existing output is neither a regular file nor a symlink: %s", outputAbs)
	}
	if in, err := filepath.EvalSymlinks(inputAbs); err == nil {
		if out, err := filepath.EvalSymlinks(outputAbs); err == nil && in == out {
			return errs.Input("output must not overwrite the input")
		}
	}
	return nil
}

// commit writes data next to path and atomically renames it into place.
func commit(path string, data []byte) error {
	dir := filepath.Dir(path)
	if st, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return errs.Runtime("cannot create output directory %s: %v", dir, err)
		}
	} else if !st.IsDir() {
		return errs.Input("output parent is not a directory: %s", dir)
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".md2pdf-"+hex.EncodeToString(rnd[:])+".tmp")
	mode := os.FileMode(0o666)
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
		mode = st.Mode().Perm()
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errs.Runtime("cannot create temporary output in %s: %v", dir, err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(tmp)
		return errs.Runtime("cannot write temporary output: %v", firstNonNil(werr, serr, cerr))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return errs.Runtime("cannot move output into place: %v", err)
	}
	return nil
}

func firstNonNil(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
