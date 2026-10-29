package document

import (
	"io/fs"

	"github.com/c1t0d0s0/md2pdf/internal/assets"
)

func assetsReadFile(name string) ([]byte, error) { return fs.ReadFile(assets.FS(), name) }

// initScript runs before any template or document script. It provides
// pfpdf.registerReady(promise) so raw HTML scripts can delay printing, and
// records uncaught errors and local resources that failed to load.
const initScript = `(() => {
  'use strict';
  const pending = [];
  const problems = [];
  let sealed = false;
  const record = (message) => { problems.push(String(message)); };
  addEventListener('error', (event) => {
    const target = event.target;
    if (target && target !== window) {
      const url = target.currentSrc || target.src || target.href || '';
      if (url.startsWith('file:')) record('local resource failed to load: ' + decodeURI(url));
      return;
    }
    record('document script error: ' + (event.error && event.error.message || event.message));
  }, true);
  addEventListener('unhandledrejection', (event) => {
    const r = event.reason;
    record('unhandled promise rejection: ' + (r && r.message || r));
  });
  window.pfpdf = Object.freeze({
    registerReady(value) {
      if (sealed) throw new Error('pfpdf.registerReady() was called after document parsing');
      pending.push(Promise.resolve(value));
    },
  });
  window.__md2pdfInit = Object.freeze({
    problems,
    seal() { sealed = true; return Promise.all(pending); },
  });
})();`

// mathJaxConfig renders TeX on demand to self-contained SVG. The glyph
// cache is disabled so each formula keeps its own path data.
const mathJaxConfig = `window.MathJax = {
  svg: { fontCache: 'none' },
  startup: { typeset: false },
  options: { enableMenu: false, enableAssistiveMml: false, enableEnrichment: false }
};`

// readinessScript converts math and Mermaid placeholders and waits for fonts
// and images to load. The promise resolves with a list of document problems;
// an empty list means the page is ready to print.
const readinessScript = `window.__md2pdfReady = (async () => {
  'use strict';
  const init = window.__md2pdfInit;
  const problems = [];
  const where = (el) => (el.getAttribute('data-file') || 'document') + ':' + (el.getAttribute('data-line') || '0');
  const clean = (el) => { for (const a of ['data-display', 'data-line', 'data-file']) el.removeAttribute(a); };
  try {
    if (document.readyState === 'loading') {
      await new Promise((resolve) => addEventListener('DOMContentLoaded', resolve, { once: true }));
    }
    try {
      await init.seal();
    } catch (e) {
      problems.push('pfpdf.registerReady() promise rejected: ' + (e && e.message || e));
    }
    const mathNodes = document.querySelectorAll('.pfpdf-math-src');
    if (mathNodes.length > 0) {
      if (!window.MathJax || !MathJax.startup) throw new Error('MathJax failed to load');
      await MathJax.startup.promise;
      for (const el of mathNodes) {
        const display = el.getAttribute('data-display') === '1';
        let node;
        try {
          node = await MathJax.tex2svgPromise(el.textContent, { display });
        } catch (e) {
          problems.push(where(el) + ': TeX error in math expression: ' + (e && e.message || e));
          continue;
        }
        const error = node.querySelector('[data-mjx-error]');
        if (error) {
          problems.push(where(el) + ': TeX error in math expression: ' + error.getAttribute('data-mjx-error'));
          continue;
        }
        clean(el);
        el.className = display ? 'pfpdf-math-display' : 'pfpdf-math-inline';
        el.replaceChildren(node);
      }
    }
    if (mathNodes.length > 0) document.head.appendChild(MathJax.svgStylesheet());
    const diagrams = document.querySelectorAll('.pfpdf-mermaid');
    if (diagrams.length > 0) {
      if (!window.mermaid) throw new Error('Mermaid failed to load');
      await document.fonts.ready;
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        theme: 'default',
        fontFamily: getComputedStyle(document.body).fontFamily,
      });
      let index = 0;
      for (const box of diagrams) {
        const pre = box.querySelector('pre.mermaid');
        if (!pre) continue;
        const id = 'pfpdf-mermaid-' + (++index);
        try {
          const { svg } = await mermaid.render(id, pre.textContent);
          box.innerHTML = svg;
          clean(box);
        } catch (e) {
          problems.push(where(box) + ': Mermaid rendering failed: ' + (e && e.message || e));
          document.getElementById('d' + id)?.remove();
        }
      }
    }
    await document.fonts.ready;
    const local = Array.from(document.images).filter((img) => /^(file|data):/.test(img.currentSrc || img.src));
    await Promise.all(local.map((img) => img.decode().catch(() => {
      problems.push('local resource failed to load: ' + decodeURI(img.getAttribute('src') || ''));
    })));
  } catch (e) {
    problems.push('document script error: ' + (e && e.message || e));
  }
  return [...init.problems, ...problems];
})();`
