package okf

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"go.kenn.io/docbank/document/bundle"
)

func TestRenderOKFBodyAndStrings(t *testing.T) {
	body := []byte("# Heading\r\n\r\nRevenue was 120 units.  \r\n\r\n---\r\n")
	got, err := Render(Header{Title: "true", Resource: "https://example.invalid/original", SourceTitle: "0012", Tags: []string{"null", "<b>"}}, body)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.Index(got[4:], []byte("\n---\n"))
	if end < 0 {
		t.Fatal("missing closing delimiter")
	}
	end += 4
	if !bytes.Equal(got[end+5:], body) {
		t.Fatalf("body changed: %q", got[end+5:])
	}
	var doc struct {
		Type        string   `yaml:"type"`
		Title       string   `yaml:"title"`
		Description string   `yaml:"description"`
		Tags        []string `yaml:"tags"`
		Sources     []struct {
			ID    string `yaml:"id"`
			Title string `yaml:"title"`
		} `yaml:"sources"`
	}
	if err := yaml.Unmarshal(got[4:end], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "Reference" || doc.Title != "true" || doc.Description != "Revenue was 120 units." || len(doc.Tags) != 2 || doc.Tags[0] != "null" || doc.Tags[1] != "<b>" || len(doc.Sources) != 1 || doc.Sources[0].ID != "original" || doc.Sources[0].Title != "0012" {
		t.Fatalf("unexpected header: %+v", doc)
	}
}

func TestDescriptionOKF(t *testing.T) {
	body := []byte("# Title\n\n```\nignore\n```\n\n| `col` |\n| --- |\n\n<b>ignore</b>\n\n" + strings.Repeat("界", 300))
	if got := Description(body); got != strings.Repeat("界", 280) {
		t.Fatalf("description = %q", got)
	}
}

func TestRenderOKFGoldenAndLimits(t *testing.T) {
	body := []byte("# Quarterly statement\n\nRevenue was 120 units.\n")
	h := Header{Title: "Quarterly statement", Resource: "https://archive.example.invalid/api/v1/versions/22222222-2222-4222-8222-222222222222/content", SourceTitle: "Quarterly statement"}
	got, err := Render(h, body)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntype: \"Reference\"\ntitle: \"Quarterly statement\"\ndescription: \"Revenue was 120 units.\"\nsources:\n  - id: \"original\"\n    resource: \"https://archive.example.invalid/api/v1/versions/22222222-2222-4222-8222-222222222222/content\"\n    title: \"Quarterly statement\"\n---\n" + string(body)
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	h.Tags = make([]string, 257)
	if _, err := Render(h, body); err == nil {
		t.Fatal("accepted too many tags")
	}
}

func TestDescriptionSkipsHTMLBlock(t *testing.T) {
	body := []byte("<div>\nnot prose\n</div>\n\nReal   paragraph\ncontinues here.\n")
	if got := Description(body); got != "Real paragraph continues here." {
		t.Fatalf("description = %q", got)
	}
}

func TestDescriptionMarkdownProse(t *testing.T) {
	cases := []struct{ body, want string }{
		{"````\n```\nsecret code\n````\n\nActual **prose** with [link](https://example.invalid) &amp; escape\\.\n", "Actual prose with link & escape."},
		{"    indented code\n\nOrdinary text\n", "Ordinary text"},
		{"````\n```\ncode\n````\n", ""},
	}
	for _, tc := range cases {
		if got := Description([]byte(tc.body)); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestOKFEmptyAndEscaping(t *testing.T) {
	h := Header{Title: "<b>", Resource: "https://example.invalid", Tags: []string{"true", "null", "0012", "1e3", ": x"}, Generated: &bundle.OKFGenerated{By: "producer/version", At: "2026-01-01T00:00:00Z"}}
	got, err := Render(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.Index(got[4:], []byte("\n---\n"))
	if end < 0 {
		t.Fatal("missing delimiter")
	}
	end += 4
	if len(got[end+5:]) != 0 {
		t.Fatal("empty body changed")
	}
	var value yaml.Node
	if err := yaml.Unmarshal(got[4:end], &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Content) != 1 || value.Content[0].Kind != yaml.MappingNode {
		t.Fatal("not a mapping")
	}
	seen := map[string]bool{}
	for i := 0; i < len(value.Content[0].Content); i += 2 {
		k := value.Content[0].Content[i].Value
		if seen[k] {
			t.Fatalf("duplicate %q", k)
		}
		seen[k] = true
	}
	if !strings.Contains(string(got), `by: "producer/version"`) {
		t.Fatal("actor missing")
	}
}

func TestOKFTagNameByteLimit(t *testing.T) {
	h := Header{Title: "Synthetic", Resource: "https://example.invalid", Tags: []string{strings.Repeat("x", 16<<10)}}
	if _, err := Render(h, nil); err != nil {
		t.Fatal(err)
	}
	h.Tags[0] += "x"
	if _, err := Render(h, nil); err == nil || !strings.Contains(err.Error(), "tag names") {
		t.Fatalf("error = %v", err)
	}
}

func TestDescriptionAfterNonProse(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"![alt](https://example.invalid/image)\n\nMeaningful prose.\n", "Meaningful prose."},
		{"`code only`\n\nMeaningful prose.\n", "Meaningful prose."},
		{"![alt](https://example.invalid/image)\n\n`code`\n\n" + strings.Repeat("界", 300), strings.Repeat("界", 280)},
		{"![alt](https://example.invalid/image)\n\n`code`\n", ""},
	} {
		t.Run(tc.body[:min(len(tc.body), 16)], func(t *testing.T) {
			body := []byte(tc.body)
			if got := Description(body); got != tc.want {
				t.Errorf("description = %q", got)
			}
			rendered, err := Render(Header{Title: "Synthetic", Resource: "https://example.invalid"}, body)
			if err != nil {
				t.Fatal(err)
			}
			end := bytes.Index(rendered[4:], []byte("\n---\n"))
			if end < 0 || !bytes.Equal(rendered[end+9:], body) {
				t.Fatalf("body changed: %q", rendered)
			}
		})
	}
}

func TestDescriptionScanBound(t *testing.T) {
	late := []byte(strings.Repeat("![image](https://example.invalid/i)\n\n", 2000) + "Late prose.\n")
	if len(late) <= descriptionScanBytes {
		t.Fatal("fixture must exceed scan bound")
	}
	if got := Description(late); got != "" {
		t.Fatalf("description beyond scan bound = %q", got)
	}
	partial := []byte("![image](https://example.invalid/i)\n\n" + strings.Repeat("x", descriptionScanBytes))
	if got := Description(partial); got != "" {
		t.Fatalf("partial paragraph included = %q", got)
	}
	straddled := []byte("First paragraph.\n\n" + strings.Repeat("x", descriptionScanBytes))
	if got := Description(straddled); got != "First paragraph." {
		t.Fatalf("description before block boundary = %q", got)
	}
	multi := []byte(strings.Repeat("界", descriptionScanBytes/3+2))
	if got := Description(multi); got != strings.Repeat("界", 280) {
		t.Fatalf("multibyte description = %q", got)
	}
	rendered, err := Render(Header{Title: "Synthetic", Resource: "https://example.invalid"}, late)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.Index(rendered[4:], []byte("\n---\n"))
	if end < 0 || !bytes.Equal(rendered[end+9:], late) {
		t.Fatal("render changed body beyond scan bound")
	}
}

func TestRenderYAMLScalarRoundTrip(t *testing.T) {
	value := "<tag>& punctuation\x00\x01\n\t\\\" 世界"
	body := []byte("# Synthetic\r\n\r\nExact body.\n")
	got, err := Render(Header{Title: value, Resource: "https://example.invalid/?a=1&b=2", SourceTitle: value, Tags: []string{value}}, body)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.Index(got[4:], []byte("\n---\n"))
	if end < 0 || !bytes.Equal(got[end+9:], body) {
		t.Fatal("body changed")
	}
	var parsed struct {
		Title   string   `yaml:"title"`
		Tags    []string `yaml:"tags"`
		Sources []struct {
			Title    string `yaml:"title"`
			Resource string `yaml:"resource"`
		} `yaml:"sources"`
	}
	if err := yaml.Unmarshal(got[4:end+4], &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Title != value || len(parsed.Tags) != 1 || parsed.Tags[0] != value || len(parsed.Sources) != 1 || parsed.Sources[0].Title != value || parsed.Sources[0].Resource != "https://example.invalid/?a=1&b=2" {
		t.Fatalf("YAML round trip = %+v", parsed)
	}
	if !strings.Contains(string(got), `<tag>& punctuation`) || !strings.Contains(string(got), `?a=1&b=2`) {
		t.Fatalf("markup escaped in header: %s", got[:end+9])
	}
}

func TestRenderYAMLLiteralMarkupGolden(t *testing.T) {
	body := []byte("Visible prose.\n")
	got, err := Render(Header{Title: "<b>&", Resource: "https://example.invalid/?a=1&b=2", Tags: []string{"<tag>&"}}, body)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntype: \"Reference\"\ntitle: \"<b>&\"\ndescription: \"Visible prose.\"\ntags:\n  - \"<tag>&\"\nsources:\n  - id: \"original\"\n    resource: \"https://example.invalid/?a=1&b=2\"\n---\n" + string(body)
	if string(got) != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestDescriptionNestedProse(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"- Invoice subtotal: 12 units.\n- Tax: 2 units.\n", "Invoice subtotal: 12 units."},
		{"> Meaningful blockquote prose.\n", "Meaningful blockquote prose."},
		{"1. Ordered prose.\n2. More.\n", "Ordered prose."},
		{"> > Nested quote.\n", "Nested quote."},
		{"# Heading only\n", ""},
		{"```\ncode only\n```\n", ""},
		{strings.Repeat("a", 300) + " <b>html</b>\n\nNext prose.\n", "Next prose."},
		{"| Revenue | Tax |\n| --- | --- |\n| 12 | 2 |\n", "Revenue"},
		{"> - `code`\n> - Nested prose.\n", "Nested prose."},
		{"# Heading\n\n```\nignore code\n```\n\n<div>ignore html</div>\n\n- ![image](https://example.invalid/i)\n- Usable prose.\n", "Usable prose."},
	} {
		if got := Description([]byte(tc.body)); got != tc.want {
			t.Errorf("body %q got %q want %q", tc.body, got, tc.want)
		}
	}
}

func TestDescriptionInvisibleAndReference(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"\u200b\u200d\n\nVisible prose.\n", "Visible prose."},
		{"Family 👩‍👩‍👦 text.\n", "Family 👩‍👩‍👦 text."},
	} {
		if got := Description([]byte(tc.body)); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.body, got, tc.want)
		}
	}
}
