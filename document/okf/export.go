// Package okf renders retained rendition bodies as OKF reference concepts.
package okf

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
)

const maxHeaderBytes = 256 << 10
const maxTags = 256
const maxTagBytes = 16 << 10

type Generated struct{ By, At string }
type Header struct {
	Title, Resource, SourceTitle, SourceSHA256 string
	Tags                                       []string
	Generated                                  *Generated
}

var yamlEscaper = strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`)

func quote(s string) string { return yamlEscaper.Replace(strconv.Quote(s)) }

// Render emits one YAML header followed by the exact retained body bytes.
func Render(h Header, body []byte) ([]byte, error) {
	if len(body) > document.MaxRenditionMarkdownBytes || len(h.Tags) > maxTags {
		return nil, bundle.ErrLimit
	}
	size := len(h.Title) + len(h.Resource) + len(h.SourceTitle) + len(h.SourceSHA256)
	tagBytes := 0
	for _, tag := range h.Tags {
		tagBytes += len(tag)
	}
	if tagBytes > maxTagBytes || size+tagBytes > maxHeaderBytes {
		return nil, bundle.ErrLimit
	}
	if h.Title == "" || h.Resource == "" || !utf8.ValidString(h.Title) || !utf8.ValidString(h.Resource) {
		return nil, fmt.Errorf("%w: invalid OKF header", bundle.ErrConflict)
	}
	var b strings.Builder
	b.WriteString("---\ntype: \"Reference\"\ntitle: ")
	b.WriteString(quote(h.Title))
	b.WriteByte('\n')
	if d := Description(body); d != "" {
		b.WriteString("description: ")
		b.WriteString(quote(d))
		b.WriteByte('\n')
	}
	if len(h.Tags) > 0 {
		b.WriteString("tags:\n")
		for _, tag := range h.Tags {
			b.WriteString("  - ")
			b.WriteString(quote(tag))
			b.WriteByte('\n')
		}
	}
	if h.Generated != nil {
		b.WriteString("generated:\n  by: ")
		b.WriteString(quote(h.Generated.By))
		b.WriteString("\n  at: ")
		b.WriteString(quote(h.Generated.At))
		b.WriteByte('\n')
	}
	b.WriteString("sources:\n  - id: \"original\"\n    resource: ")
	b.WriteString(quote(h.Resource))
	b.WriteByte('\n')
	if h.SourceTitle != "" {
		b.WriteString("    title: ")
		b.WriteString(quote(h.SourceTitle))
		b.WriteByte('\n')
	}
	if h.SourceSHA256 != "" {
		b.WriteString("    x-docbank-sha256: ")
		b.WriteString(quote(h.SourceSHA256))
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
	var result strings.Builder
	var count int
	var pendingSpace, html bool
	fence := ""
	started := false
	for len(body) > 0 {
		line := body
		if n := bytes.IndexByte(body, '\n'); n >= 0 {
			line, body = body[:n], body[n+1:]
		} else {
			body = nil
		}
		line = bytes.TrimSpace(line)
		if html {
			if len(line) == 0 {
				html = false
			}
			continue
		}
		if fence != "" {
			if bytes.HasPrefix(line, []byte(fence)) {
				fence = ""
			}
			continue
		}
		if len(line) == 0 {
			if started {
				break
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("```")) || bytes.HasPrefix(line, []byte("~~~")) {
			if started {
				break
			}
			fence = string(line[:3])
			continue
		}
		if line[0] == '<' {
			if started {
				break
			}
			html = true
			continue
		}
		if line[0] == '#' || line[0] == '|' || bytes.HasPrefix(line, []byte("---")) {
			if started {
				break
			}
			continue
		}
		if started {
			pendingSpace = true
		}
		started = true
		for len(line) > 0 {
			r, n := utf8.DecodeRune(line)
			line = line[n:]
			if unicode.IsSpace(r) {
				pendingSpace = true
				continue
			}
			if pendingSpace && count > 0 {
				if count == 280 {
					return result.String()
				}
				result.WriteByte(' ')
				count++
			}
			pendingSpace = false
			if count == 280 {
				return strings.TrimSpace(result.String())
			}
			result.WriteRune(r)
			count++
		}
	}
	return strings.TrimSpace(result.String())
}
