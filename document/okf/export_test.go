package okf

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderOKFBodyAndStrings(t *testing.T) {
	body := []byte("# Heading\r\n\r\nRevenue was 120 units.  \r\n---\r\n")
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
	body := []byte("# Title\n\n```\nignore\n```\n\n| col |\n| --- |\n\n<b>ignore</b>\n\n" + strings.Repeat("界", 300))
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
