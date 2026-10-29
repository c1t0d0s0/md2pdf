# Third-party components

## Embedded in the binary (`internal/assets`)

| Component | Version | License | Source |
|---|---|---|---|
| pfpdf templates (academic, book, compact, default, notebook, pfn, technical) | 0.2.1 | MIT | https://github.com/pfnet-research/pfpdf |
| Noto Sans JP / Noto Serif JP / Noto Sans Mono | google/fonts main | SIL Open Font License 1.1 (`vendor/fonts/OFL.txt`) | https://github.com/google/fonts |
| MathJax (`tex-svg-full.js`) | 3.2.2 | Apache License 2.0 | https://github.com/mathjax/MathJax |
| Mermaid (`mermaid.min.js`) | 11.12.0 | MIT | https://github.com/mermaid-js/mermaid |
| highlight.js GitHub theme (`github.css`) | 11.11.1 | BSD-3-Clause | https://github.com/highlightjs/highlight.js |

## Go modules

| Module | License |
|---|---|
| github.com/yuin/goldmark | MIT |
| github.com/alecthomas/chroma/v2 | MIT |
| github.com/chromedp/chromedp, github.com/chromedp/cdproto | MIT |
| github.com/pdfcpu/pdfcpu | Apache License 2.0 |
| github.com/rivo/uniseg | MIT |
| golang.org/x/net, golang.org/x/text | BSD-3-Clause |
| gopkg.in/yaml.v3 | MIT / Apache License 2.0 |

The browser used for rendering (Chrome / Chromium) is not bundled.
