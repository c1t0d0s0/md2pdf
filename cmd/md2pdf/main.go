// Command md2pdf converts Markdown documents into print-quality PDFs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/c1t0d0s0/md2pdf/internal/assets"
	"github.com/c1t0d0s0/md2pdf/internal/build"
	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/input"
	"github.com/c1t0d0s0/md2pdf/internal/render"
)

var version = "0.1.0"

const helpText = `Usage: md2pdf [OPTIONS] INPUT [-o OUTPUT]
       md2pdf --input INPUT --output OUTPUT [OPTIONS]

INPUT is a Markdown file, or a directory whose *.md files are combined in
file name order. OUTPUT defaults to INPUT with a .pdf extension.

Options:
  -i, --input PATH        Markdown file or directory
  -o, --output PATH       destination .pdf path
      --title TEXT        override the front matter title
      --toc / --no-toc    enable / disable the table of contents (default: on)
  -t, --template SOURCE   bundled preset name or template directory
      --logo PATH         cover logo image; overrides the template default
      --no-logo           remove the cover logo
      --font-dir PATH     extra font directory (repeatable)
      --browser-path PATH Chromium-based browser executable
      --no-sandbox        disable the browser sandbox (trusted documents only)
      --timeout DURATION  rendering deadline, e.g. 90s or 5m (default: 5m)
      --keep-work-dir     keep the temporary work directory for debugging
      --log-level LEVEL   error / warn / info / debug (default: warn)
      --list-templates    list the bundled template presets and exit
      --doctor            check that a browser is available and exit
      --version           print the version and exit
  -h, --help              print this help and exit

Environment:
  SOURCE_DATE_EPOCH       fixed timestamp for reproducible metadata and dates
  CHROME_PATH             browser executable when --browser-path is not given
`

var levels = map[string]int{"error": 0, "warn": 1, "info": 2, "debug": 3}

type logger struct{ level int }

func (l logger) emit(lvl int, name, msg string) {
	if lvl <= l.level {
		fmt.Fprintf(os.Stderr, "md2pdf: %s: %s\n", name, msg)
	}
}
func (l logger) Warn(m string)  { l.emit(1, "warn", m) }
func (l logger) Info(m string)  { l.emit(2, "info", m) }
func (l logger) Debug(m string) { l.emit(3, "debug", m) }

type options struct {
	build.Config
	mode     string
	logLevel string
}

func parseArgs(args []string) (*options, error) {
	o := &options{logLevel: "warn"}
	o.Timeout = 5 * time.Minute
	seen := map[string]bool{}
	var positional []string
	setMode := func(m string) error {
		if o.mode != "" {
			return errs.Input("command modes may not be combined: --%s, --%s", o.mode, m)
		}
		o.mode = m
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		takeValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", errs.Input("missing value for %s", name)
			}
			i++
			return args[i], nil
		}
		once := func(key string) error {
			if seen[key] {
				return errs.Input("%s may not be repeated", key)
			}
			seen[key] = true
			return nil
		}
		var err error
		var v string
		switch name {
		case "-i", "--input", "-o", "--output", "--title", "-t", "--template", "--logo",
			"--browser-path", "--timeout", "--log-level", "--font-dir":
			if v, err = takeValue(); err != nil {
				return nil, err
			}
			if strings.ContainsRune(v, 0) {
				return nil, errs.Input("%s: NUL character is not allowed", name)
			}
			canonical := map[string]string{"-i": "--input", "-o": "--output", "-t": "--template"}[name]
			if canonical == "" {
				canonical = name
			}
			if canonical != "--font-dir" {
				if err := once(canonical); err != nil {
					return nil, err
				}
			}
			if v == "" && canonical != "--title" {
				return nil, errs.Input("%s: empty value is not allowed", canonical)
			}
			switch canonical {
			case "--input":
				o.Input = v
			case "--output":
				o.Output = v
			case "--title":
				o.Title = &v
			case "--template":
				o.Template = &v
			case "--logo":
				o.Logo = &v
			case "--browser-path":
				o.BrowserPath = v
			case "--font-dir":
				o.FontDirs = append(o.FontDirs, v)
			case "--timeout":
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, errs.Input("--timeout: expected a duration such as 90s or 5m, got %q", v)
				}
				if d < time.Second || d > time.Hour {
					return nil, errs.Input("--timeout: must be between 1s and 1h")
				}
				o.Timeout = d
			case "--log-level":
				if _, ok := levels[v]; !ok {
					return nil, errs.Input("--log-level: expected error/warn/info/debug, got %q", v)
				}
				o.logLevel = v
			}
		case "--toc", "--no-toc", "--no-logo", "--no-sandbox", "--keep-work-dir",
			"--list-templates", "--doctor", "--version", "-h", "--help":
			if hasValue {
				return nil, errs.Input("%s does not take a value", name)
			}
			key := name
			if key == "-h" {
				key = "--help"
			}
			if err := once(key); err != nil {
				return nil, err
			}
			switch key {
			case "--toc", "--no-toc":
				if seen["--toc"] && seen["--no-toc"] {
					return nil, errs.Input("--toc and --no-toc may not be combined")
				}
				b := key == "--toc"
				o.TOC = &b
			case "--no-logo":
				o.NoLogo = true
			case "--no-sandbox":
				o.NoSandbox = true
			case "--keep-work-dir":
				o.KeepWorkDir = true
			default:
				if err := setMode(strings.TrimPrefix(key, "--")); err != nil {
					return nil, err
				}
			}
		default:
			return nil, errs.Input("unknown option: %s (see --help)", name)
		}
	}
	if o.Logo != nil && o.NoLogo {
		return nil, errs.Input("--logo and --no-logo may not be combined")
	}
	switch {
	case len(positional) > 1:
		return nil, errs.Input("unexpected argument: %s", positional[1])
	case len(positional) == 1 && o.Input != "":
		return nil, errs.Input("input given twice: %s and --input %s", positional[0], o.Input)
	case len(positional) == 1:
		o.Input = positional[0]
	}
	if o.Input == "-" || o.Output == "-" {
		return nil, errs.Input("stdin/stdout (\"-\") is not supported; use named paths")
	}
	if o.Title != nil {
		if err := input.ValidateTitle(*o.Title); err != nil {
			return nil, err
		}
	}
	if o.Template != nil && !assets.IsTemplate(*o.Template) {
		if st, err := os.Stat(*o.Template); err != nil || !st.IsDir() {
			return nil, errs.Input("--template: not a bundled preset or a directory: %s (presets: %s)",
				*o.Template, strings.Join(assets.TemplateNames(), ", "))
		}
	}
	if o.mode == "" {
		if o.Input == "" {
			return nil, errs.Input("an input file or directory is required (see --help)")
		}
		if o.Output == "" {
			o.Output = defaultOutput(o.Input)
		}
	}
	return o, nil
}

func defaultOutput(in string) string {
	in = strings.TrimRight(in, `/\`)
	if strings.HasSuffix(in, ".md") {
		return strings.TrimSuffix(in, ".md") + ".pdf"
	}
	return in + ".pdf"
}

func run(args []string) int {
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "md2pdf: error: %v\n", err)
		return errs.ExitCode(err)
	}
	log := logger{level: levels[o.logLevel]}
	o.Log = log

	switch o.mode {
	case "help":
		fmt.Print(helpText)
		return 0
	case "version":
		fmt.Println(version)
		return 0
	case "list-templates":
		for _, n := range assets.TemplateNames() {
			fmt.Println(n)
		}
		return 0
	case "doctor":
		return doctor(o)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := build.Run(ctx, o.Config); err != nil {
		if ctx.Err() != nil {
			err = errs.Runtime("build interrupted")
		}
		fmt.Fprintf(os.Stderr, "md2pdf: error: %v\n", err)
		return errs.ExitCode(err)
	}
	return 0
}

// doctor reports which browser would be used and checks that it launches.
func doctor(o *options) int {
	fail := func(err error) int {
		fmt.Printf("status:  failed\nerror:   %v\n", err)
		return errs.ExitCode(err)
	}
	p, err := render.FindBrowser(o.BrowserPath)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("md2pdf:  %s\nbrowser: %s\nsandbox: %v\n", version, p, !o.NoSandbox)
	s, err := render.Open(context.Background(), "about:blank", render.Options{
		BrowserPath: p,
		Timeout:     o.Timeout,
		NoSandbox:   o.NoSandbox,
	})
	if err != nil {
		return fail(err)
	}
	s.Close()
	fmt.Println("status:  ok")
	return 0
}

func main() { os.Exit(run(os.Args[1:])) }
