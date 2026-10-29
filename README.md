# md2pdf

Markdown 文書を印刷品質の PDF に変換する Go 製のコマンドラインツールです。
[pfpdf](https://github.com/pfnet-research/pfpdf)（TypeScript / Vivliostyle）の設計と
テンプレートを参考に、Go で再実装しました。日本語組版を重視しており、
GFM、表紙、目次（ページ番号付き）、改ページ、数式、コードハイライト、Mermaid、
BibTeX 参考文献、生 HTML をサポートします。

- フォント（Noto Sans JP / Noto Serif JP / Noto Sans Mono）、MathJax、Mermaid、
  テンプレートをすべてバイナリに埋め込んでいるため、単一バイナリ・オフラインで動作します
- PDF 化には手元の Chrome / Chromium（ヘッドレス）を DevTools Protocol 経由で使います
- `SOURCE_DATE_EPOCH` を指定すると、同じ入力からバイト単位で同一の PDF を生成します

## 必要なもの

- Go 1.26 以上（ビルド時のみ）
- Chrome / Chromium / Edge のいずれか（実行時）。次の順に探索します:
  `--browser-path` → 環境変数 `CHROME_PATH` → `PATH` 上の `google-chrome` / `chromium` など →
  Playwright / Puppeteer のブラウザキャッシュ（`~/.cache/ms-playwright` など）

## ビルド

```bash
make build          # 埋め込みアセットを取得（未取得の場合）して bin/md2pdf を生成
# または
./scripts/fetch-assets.sh && go build -o bin/md2pdf ./cmd/md2pdf
```

## 使い方

```bash
md2pdf document.md                       # document.pdf を出力
md2pdf document.md -o out/report.pdf
md2pdf docs/ -o docs.pdf                 # ディレクトリ直下の *.md をファイル名順に結合
md2pdf --input docs --output docs.pdf -t book --no-toc
md2pdf --doctor                          # ブラウザが起動できるか確認
```

| オプション | 説明 |
|---|---|
| `-i, --input PATH` | Markdown ファイルまたはディレクトリ（位置引数でも可） |
| `-o, --output PATH` | 出力 PDF（省略時は入力名の拡張子を `.pdf` に） |
| `--title TEXT` | front matter の title を上書き |
| `--toc` / `--no-toc` | 目次の有効 / 無効（既定: 有効） |
| `-t, --template SOURCE` | 同梱テンプレート名またはテンプレートディレクトリ |
| `--logo PATH` / `--no-logo` | 表紙ロゴの指定 / 削除 |
| `--font-dir PATH` | 追加フォントディレクトリ（複数指定可） |
| `--browser-path PATH` | 使用するブラウザ |
| `--no-sandbox` | ブラウザのサンドボックスを無効化（信頼できる文書のみ） |
| `--timeout DURATION` | レンダリングの制限時間（例: `90s`, `5m`。既定 5m） |
| `--keep-work-dir` | 一時作業ディレクトリ（生成 HTML・ブラウザ出力 PDF）を残す |
| `--log-level LEVEL` | `error` / `warn` / `info` / `debug` |
| `--list-templates` | 同梱テンプレートの一覧 |
| `--doctor` | ブラウザの検出と起動確認 |

設定の優先順位は **組み込み既定値 < front matter < CLI** です。

終了コード: `0` 成功 / `1` 実行環境・ブラウザのエラー / `2` 入力エラー
（引数、front matter、TeX・Mermaid の構文エラー、存在しないローカルファイルなど）。
エラーは `ファイル名:行番号: 内容` の形式で表示します。

## front matter

最初のファイルの先頭に YAML で記述します（`title` は必須、未知のキーはエラー）。

```yaml
---
title: 四半期レポート<br>2026 年 第 3 四半期   # <br> で改行可
author: 山田 太郎
series: 社内報告書シリーズ
date: 2026-10-01          # 省略時は SOURCE_DATE_EPOCH または実行日
page_size: A4             # A3/A4/A5/JIS-B4/JIS-B5/ISO-B4/ISO-B5/Letter/Legal または "182mm 257mm"
confidential: true        # 表紙とフッタに Confidential を表示
lang: ja                  # BCP 47。既定 ja
dir: auto                 # ltr / rtl / auto
template: technical       # 同梱テンプレート名
toc: true
logo: assets/logo.svg     # この Markdown からの相対パス。false でロゴなし
bibliography: refs.bib    # 文字列または配列
---
```

## Markdown の機能

- **GFM**: 表、タスクリスト、取り消し線、自動リンク、脚注
- **日本語向け強調**: `これは**「重要」**です` のように全角括弧に隣接しても強調になります
  （markdown-cjk-friendly の規則）。日本語の行内改行は空白になりません
- **数式**: `$...$`（インライン）、`$$...$$`（ディスプレイ）。MathJax で SVG 化します。
  `$5 and $10` のような金額は数式になりません。`\$` でエスケープできます
- **コードハイライト**: フェンスに言語名を指定（chroma で解析し highlight.js 互換の
  クラスを出力するため、hljs 用テーマがそのまま使えます）
- **Mermaid**: 言語名 `mermaid` のコードブロックを図にします
- **改ページ**: トップレベルに `___`（アンダースコア 3 つだけの行）を書くと改ページ。
  `---` や `***` は通常の水平線です
- **ファイル間リンク**: `[第 2 章](02_structure.md)` や `02_structure.md#見出し` は PDF 内リンクに変換
- **見出し ID**: 見出しテキストから自動生成（重複は `-2`, `-3`…）。目次と PDF のしおりに使われます
- **BibTeX**: `\cite{key}` / `\cite{a,b}` で番号付き引用、`\printbibliography` の位置
  （省略時は文末）に Vancouver 形式の参考文献リストを出力。`\\cite{}` はエスケープ
- **生 HTML / `<script>`**: そのまま出力します。スクリプトから
  `pfpdf.registerReady(promise)` を呼ぶと、その完了まで PDF 化を待ちます
- **長い URL・識別子**: 表示上の改行位置（`<wbr>`）を自動挿入（コード内は対象外）

## テンプレート

同梱: `academic` / `book` / `compact` / `default` / `notebook` / `pfn` / `technical`
（pfpdf のテンプレートを Chromium 向けに調整したもの）。

独自テンプレートはディレクトリで指定します（pfpdf のテンプレートもそのまま使えます）:

| ファイル | 必須 | 内容 |
|---|---|---|
| `template.html` | ○ | `html` / `head` / `body` を持つ HTML。`data-pfpdf-slot` 属性で差し込み位置を指定 |
| `style.css` | ○ | 本文のスタイル。`@import` と相対 `url()` を解決します |
| `print.css`（または `vivliostyle.css`） |  | `@page` などページ組版用のスタイル |

スロット: `content`（必須）、`title`、`author`、`series`、`date`、`confidential`、`toc`、
`logo`（`<img>` 要素）。値のないスロットは要素ごと削除されます。

Chromium が未対応の Vivliostyle / CSS GCPM 機能は次のように変換します:

| テンプレート中の記述 | md2pdf での扱い |
|---|---|
| `env(pub-title)` | 文書タイトルの文字列 |
| `string-set` / `string(name)` | スロットの値から静的に評価（Confidential、シリーズ名など） |
| `target-counter(attr(href url), page)` | 一度 PDF 化して各見出しのページを PDF から読み取り、目次に書き込んで再印刷 |
| `leader(".")` | 点線のリーダー要素で代替 |
| `position: running()` / `element()` | 非表示 / 文字列に置換 |

## 仕組み

```
Markdown ──goldmark──▶ HTML DOM ──テンプレート差し込み──▶ document.html
                                                         │ chromedp (Chrome headless)
                                                         ▼
     最終 PDF ◀── メタデータ追記（増分更新） ◀── PDF ◀── MathJax / Mermaid / 目次ページ番号
```

| パッケージ | 役割 |
|---|---|
| `internal/input` | 入力ファイルの列挙、UTF-8 検証、front matter |
| `internal/markdown` | goldmark 拡張（CJK 強調、数式、改ページ、引用）、見出し ID、リンク・リソース解決、ハイライト、改行機会の挿入 |
| `internal/bib` | BibTeX パーサと Vancouver 形式の整形 |
| `internal/document` | テンプレートのスロット充填、CSS の変換、フォント、ブラウザ側スクリプト |
| `internal/render` | ブラウザの探索・起動、読み込み完了待ち、PDF 印刷 |
| `internal/pdfpost` | PDF の名前付き宛先からのページ番号取得、メタデータ（Title / Author / Lang / 日付）の設定 |
| `internal/build` | パイプライン全体と出力のアトミックな書き込み |

## pfpdf との違い

- レンダラは Vivliostyle ではなく Chromium の印刷機能です。目次ページ番号は 2 パスで解決し、
  「目次（続き）」の柱は表示しません
- 参考文献は citation-js / CSL ではなく組み込みの Vancouver 風フォーマッタです
- テンプレートやロゴの Git リポジトリ指定（`git::URL`）、`--host-fonts` は未対応です
  （OS のフォントは Chromium のフォールバックとして使われます）
- Linux でサンドボックスが起動できない環境（Ubuntu 23.10 以降の AppArmor 制限など）では、
  明示的に `--no-sandbox` を指定できます

## 信頼モデル

md2pdf は **自分で書いた信頼できる文書** を変換するためのツールです。生 HTML や
`<script>` はサニタイズせずにブラウザで実行され、文書はローカルファイルやリモートの
リソースを参照できます。信頼できない Markdown を変換しないでください。

## テスト

```bash
make test                                   # ブラウザがあれば E2E テストも実行
MD2PDF_TEST_NO_SANDBOX=1 go test ./...      # サンドボックスが使えない環境
go test -short ./...                        # ブラウザを使わないテストのみ
```

## ライセンス

MIT License（[LICENSE](LICENSE)）。同梱テンプレートは pfpdf（MIT）由来です。
埋め込みフォント・ライブラリのライセンスは [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) を参照してください。
