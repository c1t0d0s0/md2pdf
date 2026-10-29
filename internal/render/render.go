// Package render prints HTML documents to PDF with headless Chromium via
// the DevTools protocol.
package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/c1t0d0s0/md2pdf/internal/errs"
)

// Session is a document loaded in a browser tab.
type Session struct {
	ctx    context.Context
	cancel func()
}

// Options controls how the browser is launched.
type Options struct {
	BrowserPath string
	Timeout     time.Duration
	NoSandbox   bool
	Debugf      func(string, ...any)
}

// FindBrowser locates a Chromium-based browser. It checks, in order, the
// explicit path, the CHROME_PATH environment variable, well-known
// executables on PATH, and the Playwright / Puppeteer browser caches.
func FindBrowser(explicit string) (string, error) {
	if explicit != "" {
		if st, err := os.Stat(explicit); err != nil || st.IsDir() {
			return "", errs.Input("--browser-path: browser executable not found: %s", explicit)
		}
		return explicit, nil
	}
	if p := os.Getenv("CHROME_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	var names []string
	switch runtime.GOOS {
	case "darwin":
		names = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				names = append(names,
					filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	}
	names = append(names, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "microsoft-edge", "headless_shell")
	for _, n := range names {
		if filepath.IsAbs(n) {
			if _, err := os.Stat(n); err == nil {
				return n, nil
			}
			continue
		}
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	if p := findCachedBrowser(); p != "" {
		return p, nil
	}
	return "", errs.Runtime("no Chromium-based browser found; install Google Chrome or Chromium, or pass --browser-path / set CHROME_PATH")
}

func findCachedBrowser() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var patterns []string
	switch runtime.GOOS {
	case "darwin":
		patterns = []string{
			filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac*/Chromium.app/Contents/MacOS/Chromium"),
			filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
			filepath.Join(home, ".cache/puppeteer/chrome/*/chrome-mac*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		patterns = []string{
			filepath.Join(local, `ms-playwright\chromium-*\chrome-win*\chrome.exe`),
			filepath.Join(home, `.cache\puppeteer\chrome\*\chrome-win64\chrome.exe`),
		}
	default:
		patterns = []string{
			filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux*/chrome"),
			filepath.Join(home, ".cache/puppeteer/chrome/*/chrome-linux64/chrome"),
			filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-*/chrome-headless-shell"),
			filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-*/headless_shell"),
		}
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		sort.Sort(sort.Reverse(sort.StringSlice(matches))) // newest revision first
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// WorkDirBase returns the parent directory for files the browser must read,
// or "" for the system temporary directory. Snap-packaged browsers have a
// private /tmp and cannot read hidden directories, so their files go in the
// snap's per-user common directory instead.
func WorkDirBase(browser string) (string, error) {
	name := snapName(browser)
	if name == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errs.Runtime("cannot locate the home directory for the %s snap: %v", name, err)
	}
	dir := filepath.Join(home, "snap", name, "common")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", errs.Runtime("cannot create work directory for the %s snap: %v", name, err)
	}
	return dir, nil
}

var snapBinRef = regexp.MustCompile(`/snap/bin/([A-Za-z0-9][A-Za-z0-9-]*)`)

// snapName returns the name of the snap that provides browser, or "" if it
// is not a snap. It recognizes /snap/bin entries, symlinks into snapd, and
// shell wrappers such as Ubuntu's /usr/bin/chromium-browser.
func snapName(browser string) string {
	fromBin := func(p string) string {
		if rest, ok := strings.CutPrefix(p, "/snap/bin/"); ok {
			name, _, _ := strings.Cut(rest, ".") // chromium.chromedriver -> chromium
			return name
		}
		return ""
	}
	if name := fromBin(browser); name != "" {
		return name
	}
	if real, err := filepath.EvalSymlinks(browser); err == nil {
		if name := fromBin(real); name != "" {
			return name
		}
		if b := filepath.Base(real); b == "snap" || b == "snapd" {
			return filepath.Base(browser)
		}
	}
	f, err := os.Open(browser)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 4<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if !bytes.HasPrefix(head, []byte("#!")) {
		return ""
	}
	if m := snapBinRef.FindSubmatch(head); m != nil {
		return string(m[1])
	}
	return ""
}

// Open launches the browser, loads documentURL, and waits for the page's
// readiness promise (window.__md2pdfReady) to settle. Document errors
// reported by that promise are returned as input errors.
func Open(parent context.Context, documentURL string, opts Options) (*Session, error) {
	browser, err := FindBrowser(opts.BrowserPath)
	if err != nil {
		return nil, err
	}
	if opts.Debugf != nil {
		opts.Debugf("browser: %s", browser)
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browser),
		chromedp.Flag("allow-file-access-from-files", true),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("hide-scrollbars", true),
	)
	if opts.NoSandbox {
		allocOpts = append(allocOpts, chromedp.NoSandbox)
	}
	output := &boundedBuffer{limit: 64 << 10}
	allocOpts = append(allocOpts, chromedp.CombinedOutput(output))
	ctx, cancelTimeout := context.WithTimeout(parent, opts.Timeout)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	s := &Session{ctx: tabCtx, cancel: func() { cancelTab(); cancelAlloc(); cancelTimeout() }}

	// Launch the browser exactly once. If the launch fails, later actions on
	// the same context must not retry it, since a retry would allocate a
	// second browser.
	if err := chromedp.Do(tabCtx); err != nil {
		s.Close()
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errs.Runtime("browser did not start within %s", opts.Timeout)
		}
		log := output.String()
		if strings.Contains(log, "No usable sandbox") {
			return nil, errs.Runtime("the browser sandbox cannot start in this environment " +
				"(for example, Ubuntu 23.10+ restricts unprivileged user namespaces); " +
				"fix the host configuration or, for trusted documents only, pass --no-sandbox")
		}
		if opts.Debugf != nil && log != "" {
			opts.Debugf("browser output:\n%s", log)
		}
		return nil, errs.Runtime("cannot start the browser %s: %v", browser, err)
	}

	messages := chromedp.Console(tabCtx)
	var mu sync.Mutex
	var consoleErrors []string
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			if m.IsException() || m.Type == chromedp.ConsoleError {
				mu.Lock()
				consoleErrors = append(consoleErrors, m.Text)
				mu.Unlock()
				if opts.Debugf != nil {
					opts.Debugf("browser console: %s", m.String())
				}
			}
		}
	}()

	err = chromedp.Do(tabCtx, chromedp.Navigate(documentURL))
	var problems []string
	if err == nil {
		problems, err = chromedp.Run(tabCtx, chromedp.Evaluate[[]string](`window.__md2pdfReady`, chromedp.EvalAwaitPromise))
	}
	if err != nil {
		s.Close()
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errs.Runtime("render timed out after %s", opts.Timeout)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(consoleErrors) > 0 {
			return nil, errs.Runtime("browser failed to load the document: %v (%s)", err, strings.Join(consoleErrors, "; "))
		}
		return nil, errs.Runtime("browser failed to load the document: %v", err)
	}
	if len(problems) > 0 {
		s.Close()
		return nil, errs.Input("%s", strings.Join(problems, "\n"))
	}
	return s, nil
}

// Eval runs a JavaScript expression in the page and discards the result.
func (s *Session) Eval(expr string) error {
	if err := chromedp.Do(s.ctx, chromedp.Evaluate[chromedp.Void](expr)); err != nil {
		return errs.Runtime("page script failed: %v", err)
	}
	return nil
}

// SetTOCPages writes resolved page numbers into the table of contents.
func (s *Session) SetTOCPages(pages map[string]int) error {
	data, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	expr := fmt.Sprintf(`(() => {
  const pages = %s;
  for (const a of document.querySelectorAll('.pfpdf-toc a[href^="#"]')) {
    const id = decodeURIComponent(a.getAttribute('href').slice(1));
    if (id in pages) a.setAttribute('data-md2pdf-page', String(pages[id]));
  }
  return true;
})()`, data)
	return s.Eval(expr)
}

// PrintPDF prints the current page using the CSS page size.
func (s *Session) PrintPDF() ([]byte, error) {
	buf, err := chromedp.Run(s.ctx, chromedp.PrintToPDF(
		chromedp.PDFPreferCSSPageSize(),
		chromedp.PDFPrintBackground(),
		chromedp.PDFMargin(0),
		chromedp.PDFOutlineAndTagged(),
	))
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, errs.Runtime("render timed out while printing")
		}
		return nil, errs.Runtime("printing to PDF failed: %v", err)
	}
	return buf, nil
}

// Close shuts down the browser.
func (s *Session) Close() { s.cancel() }

// boundedBuffer keeps the first limit bytes of browser output.
type boundedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
