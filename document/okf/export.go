// Package okf renders retained rendition bodies as OKF reference concepts.
package okf

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"unicode/utf8"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
)

const descriptionScanBytes = 64 << 10
const maxHeaderBytes = 256 << 10
const maxTags = 256
const maxTagBytes = 16 << 10

type Generated struct{ By, At string }
type Header struct {
	Title, Resource, SourceTitle, SourceSHA256 string
	Tags                                       []string
	Generated                                  *Generated
}

// Render emits one YAML header followed by the exact retained body bytes.
func Render(h Header, body []byte) ([]byte, error) {
	if len(body) > document.MaxRenditionMarkdownBytes {
		return nil, bundle.ErrLimit
	}
	if len(h.Tags) > maxTags {
		return nil, fmt.Errorf("%w: OKF tags exceed 256 entries", bundle.ErrLimit)
	}
	size := len(h.Title) + len(h.Resource) + len(h.SourceTitle) + len(h.SourceSHA256)
	tagBytes := 0
	for _, tag := range h.Tags {
		tagBytes += len(tag)
	}
	if tagBytes > maxTagBytes {
		return nil, fmt.Errorf("%w: OKF tag names exceed 16 KiB", bundle.ErrLimit)
	}
	if size+tagBytes > maxHeaderBytes {
		return nil, bundle.ErrLimit
	}
	if h.Title == "" || h.Resource == "" || !utf8.ValidString(h.Title) || !utf8.ValidString(h.Resource) {
		return nil, fmt.Errorf("%w: invalid OKF header", bundle.ErrConflict)
	}
	var b strings.Builder
	b.WriteString("---\ntype: \"Reference\"\ntitle: ")
	b.WriteString(strconv.Quote(h.Title))
	b.WriteByte('\n')
	if d := Description(body); d != "" {
		b.WriteString("description: ")
		b.WriteString(strconv.Quote(d))
		b.WriteByte('\n')
	}
	if len(h.Tags) > 0 {
		b.WriteString("tags:\n")
		for _, tag := range h.Tags {
			b.WriteString("  - ")
			b.WriteString(strconv.Quote(tag))
			b.WriteByte('\n')
		}
	}
	if h.Generated != nil {
		b.WriteString("generated:\n  by: ")
		b.WriteString(strconv.Quote(h.Generated.By))
		b.WriteString("\n  at: ")
		b.WriteString(strconv.Quote(h.Generated.At))
		b.WriteByte('\n')
	}
	b.WriteString("sources:\n  - id: \"original\"\n    resource: ")
	b.WriteString(strconv.Quote(h.Resource))
	b.WriteByte('\n')
	if h.SourceTitle != "" {
		b.WriteString("    title: ")
		b.WriteString(strconv.Quote(h.SourceTitle))
		b.WriteByte('\n')
	}
	if h.SourceSHA256 != "" {
		b.WriteString("    x-docbank-sha256: ")
		b.WriteString(strconv.Quote(h.SourceSHA256))
		b.WriteByte('\n')
	}
	b.WriteString("---\n")
	if b.Len() > maxHeaderBytes || int64(b.Len())+int64(len(body)) > bundle.MaxRoleBytes {
		return nil, bundle.ErrLimit
	}
	out := make([]byte, 0, b.Len()+len(body))
	out = append(out, b.String()...)
	out = append(out, body...)
	return out, nil
}

// Description selects the first ordinary paragraph, folded and rune-bounded.
func Description(body []byte) string {
	if len(body) > descriptionScanBytes {
		cut := descriptionScanBytes
		if boundary := bytes.LastIndex(body[:cut], []byte("\n\n")); boundary >= 0 {
			cut = boundary
		} else {
			for cut > 0 && !utf8.RuneStart(body[cut]) {
				cut--
			}
		}
		body = body[:cut]
	}
	root := parser.New(parser.WithExtensions(extension.NewGFMParser())).Parse(body)
	for paragraph := root.FirstChild(); paragraph != nil; paragraph = paragraph.NextSibling() {
		if paragraph.Kind() != ast.KindParagraph {
			continue
		}
		hasHTML := false
		_ = ast.Walk(paragraph, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering && child.Kind() == ast.KindRawHTML {
				hasHTML = true
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
		if hasHTML {
			continue
		}
		if result := descriptionParagraph(paragraph, body); result != "" {
			return result
		}
	}
	return ""
}

func descriptionParagraph(paragraph ast.Node, body []byte) string {
	var result strings.Builder
	count := 0
	space := false
	_ = ast.Walk(paragraph, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindRawHTML, ast.KindCodeSpan, ast.KindImage:
			return ast.WalkSkipChildren, nil
		}
		value, ok := n.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		for _, r := range value.Value.Value(body) {
			if unicode.IsSpace(r) {
				space = true
				continue
			}
			if space && count > 0 {
				if count >= 280 {
					return ast.WalkStop, nil
				}
				result.WriteByte(' ')
				count++
			}
			space = false
			if count >= 280 {
				return ast.WalkStop, nil
			}
			result.WriteRune(r)
			count++
		}
		if value.SoftLineBreak() || value.HardLineBreak() {
			space = true
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(result.String())
}
