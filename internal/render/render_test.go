package render

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapName(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wrapper := write("chromium-browser", "#!/bin/sh\nif ! [ -x /snap/bin/chromium ]; then\n  exit 1\nfi\nexec /snap/bin/chromium \"$@\"\n")
	script := write("chrome-wrapper", "#!/bin/sh\nexec /opt/google/chrome/chrome \"$@\"\n")
	binary := write("chrome", "\x7fELF /snap/bin/chromium")

	for _, tc := range []struct{ path, want string }{
		{"/snap/bin/chromium", "chromium"},
		{"/snap/bin/chromium.chromedriver", "chromium"},
		{wrapper, "chromium"},
		{script, ""},
		{binary, ""},
		{filepath.Join(dir, "missing"), ""},
	} {
		if got := snapName(tc.path); got != tc.want {
			t.Errorf("snapName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
