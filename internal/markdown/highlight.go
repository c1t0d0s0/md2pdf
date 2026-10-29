package markdown

import (
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"golang.org/x/net/html"
)

// highlight tokenizes code with chroma and returns DOM nodes using highlight.js
// class names, so template stylesheets written for hljs themes work as is.
// ok is false when the language is unknown.
func highlight(lang, code string) (nodes []*html.Node, ok bool) {
	lexer := lexers.Get(lang)
	if lexer == nil {
		return nil, false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(&chroma.TokeniseOptions{State: "root"}, code)
	if err != nil {
		return nil, false
	}
	var curClass, curText string
	flush := func() {
		if curText == "" {
			return
		}
		t := &html.Node{Type: html.TextNode, Data: curText}
		if curClass == "" {
			nodes = append(nodes, t)
		} else {
			span := &html.Node{Type: html.ElementNode, Data: "span", Attr: []html.Attribute{{Key: "class", Val: curClass}}}
			span.AppendChild(t)
			nodes = append(nodes, span)
		}
		curText = ""
	}
	for tok := it(); tok != chroma.EOF; tok = it() {
		cls := hljsClass(tok.Type)
		if cls != curClass {
			flush()
			curClass = cls
		}
		curText += tok.Value
	}
	flush()
	// chroma guarantees a trailing newline, but the source text is left unchanged.
	if len(code) > 0 && code[len(code)-1] != '\n' && len(nodes) > 0 {
		trimTrailingNewline(nodes[len(nodes)-1])
	}
	return nodes, true
}

func trimTrailingNewline(n *html.Node) {
	if n.Type == html.ElementNode {
		n = n.LastChild
	}
	if n != nil && n.Type == html.TextNode && len(n.Data) > 0 && n.Data[len(n.Data)-1] == '\n' {
		n.Data = n.Data[:len(n.Data)-1]
	}
}

var exactClasses = map[chroma.TokenType]string{
	chroma.KeywordType:           "hljs-type",
	chroma.KeywordConstant:       "hljs-literal",
	chroma.NameBuiltin:           "hljs-built_in",
	chroma.NameBuiltinPseudo:     "hljs-built_in",
	chroma.NameClass:             "hljs-title class_",
	chroma.NameException:         "hljs-title class_",
	chroma.NameNamespace:         "hljs-title class_",
	chroma.NameFunction:          "hljs-title function_",
	chroma.NameFunctionMagic:     "hljs-title function_",
	chroma.NameAttribute:         "hljs-attr",
	chroma.NameProperty:          "hljs-attr",
	chroma.NameTag:               "hljs-name",
	chroma.NameDecorator:         "hljs-meta",
	chroma.NameConstant:          "hljs-variable constant_",
	chroma.NameLabel:             "hljs-symbol",
	chroma.NameEntity:            "hljs-symbol",
	chroma.LiteralStringRegex:    "hljs-regexp",
	chroma.LiteralStringSymbol:   "hljs-symbol",
	chroma.LiteralStringInterpol: "hljs-subst",
	chroma.OperatorWord:          "hljs-keyword",
	chroma.CommentPreproc:        "hljs-meta",
	chroma.CommentPreprocFile:    "hljs-meta",
	chroma.CommentHashbang:       "hljs-meta",
	chroma.GenericDeleted:        "hljs-deletion",
	chroma.GenericInserted:       "hljs-addition",
	chroma.GenericHeading:        "hljs-section",
	chroma.GenericSubheading:     "hljs-section",
	chroma.GenericEmph:           "hljs-emphasis",
	chroma.GenericStrong:         "hljs-strong",
	chroma.GenericPrompt:         "hljs-meta",
	chroma.LiteralDate:           "hljs-literal",
}

var subCategoryClasses = map[chroma.TokenType]string{
	chroma.NameVariable:  "hljs-variable",
	chroma.LiteralString: "hljs-string",
	chroma.LiteralNumber: "hljs-number",
}

var categoryClasses = map[chroma.TokenType]string{
	chroma.Keyword:  "hljs-keyword",
	chroma.Comment:  "hljs-comment",
	chroma.Operator: "hljs-operator",
	chroma.Literal:  "hljs-literal",
}

func hljsClass(t chroma.TokenType) string {
	if c, ok := exactClasses[t]; ok {
		return c
	}
	if c, ok := subCategoryClasses[t.SubCategory()]; ok {
		return c
	}
	if c, ok := categoryClasses[t.Category()]; ok {
		return c
	}
	return ""
}
