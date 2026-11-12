package processing

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"go.kenn.io/docbank/document/notemarkdown"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
)

var (
	ErrNoteRange           = errors.New("invalid note range")
	ErrNoteHeadingNotFound = errors.New("note heading not found")
)

type NoteHeadingAmbiguous struct{ Candidates []string }

func (e *NoteHeadingAmbiguous) Error() string { return "note heading is ambiguous" }

type NoteTextOptions struct {
	Heading            string
	LineStart, LineEnd int
	Offset             *int
	MaxBytes           int
}

type NoteTextSelection struct {
	Text               string
	Start, End         int
	LineStart, LineEnd int
	Truncated          bool
	Heading            *notemarkdown.Heading
}

// ReadNoteOriginal reads and parses only the requested immutable original.
func ReadNoteOriginal(ctx context.Context, blobs *blob.Store, view store.NoteVersionView) ([]byte, notemarkdown.Note, string, error) {
	target := store.NoteTarget{VersionID: view.Version.ID, BlobHash: view.Version.BlobHash, Size: view.Version.Size}
	src, err := loadNoteOriginal(ctx, blobs, target)
	if err != nil {
		return nil, notemarkdown.Note{}, "", err
	}
	note, state, err := parseNoteOriginal(ctx, src)
	return src, note, state, err
}

// SelectNoteText slices verified UTF-8 original bytes, preserving absolute offsets.
func SelectNoteText(src []byte, note notemarkdown.Note, opts NoteTextOptions) (NoteTextSelection, error) {
	start, end := 0, len(src)
	var heading *notemarkdown.Heading
	if opts.Heading != "" {
		if opts.LineStart != 0 || opts.LineEnd != 0 {
			return NoteTextSelection{}, ErrNoteRange
		}
		var matches []notemarkdown.Heading
		for tier := range 3 {
			for _, h := range note.Headings {
				if (tier == 0 && h.Path == opts.Heading) || (tier == 1 && h.Text == opts.Heading) || (tier == 2 && h.Slug == opts.Heading) {
					matches = append(matches, h)
				}
			}
			if len(matches) != 0 {
				break
			}
		}
		if len(matches) == 0 {
			return NoteTextSelection{}, ErrNoteHeadingNotFound
		}
		if len(matches) > 1 {
			candidates := make([]string, 0, min(len(matches), 5))
			for _, h := range matches[:min(len(matches), 5)] {
				candidates = append(candidates, h.Path)
			}
			return NoteTextSelection{}, &NoteHeadingAmbiguous{Candidates: candidates}
		}
		heading = &matches[0]
		start, end = heading.Start, heading.SectionEnd
	}
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\r' {
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		} else if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	if opts.Heading == "" && (opts.LineStart != 0 || opts.LineEnd != 0) {
		first, last := opts.LineStart, opts.LineEnd
		if first == 0 {
			first = 1
		}
		if last == 0 {
			last = len(starts)
		}
		if first < 1 || last < first || last > len(starts) {
			return NoteTextSelection{}, ErrNoteRange
		}
		start = starts[first-1]
		if last < len(starts) {
			end = starts[last]
		}
	}
	if opts.Offset != nil {
		if *opts.Offset < start || *opts.Offset > end || (*opts.Offset < len(src) && !utf8.RuneStart(src[*opts.Offset])) {
			return NoteTextSelection{}, ErrNoteRange
		}
		start = *opts.Offset
	}
	limit := opts.MaxBytes
	if limit == 0 {
		limit = 262144
	}
	if limit < 1 || limit > 524288 {
		return NoteTextSelection{}, ErrNoteRange
	}
	actualEnd := end
	if end-start > limit {
		actualEnd = start + limit
		for actualEnd > start && !utf8.RuneStart(src[actualEnd]) {
			actualEnd--
		}
		if actualEnd == start {
			return NoteTextSelection{}, fmt.Errorf("%w: byte limit splits next rune", ErrNoteRange)
		}
	}
	lineStart, lineEnd := 1, 1
	for i, pos := range starts {
		if pos <= start {
			lineStart = i + 1
		}
		if pos < actualEnd || (pos == actualEnd && start == actualEnd) {
			lineEnd = i + 1
		}
	}
	return NoteTextSelection{Text: string(src[start:actualEnd]), Start: start, End: actualEnd, LineStart: lineStart, LineEnd: lineEnd, Truncated: actualEnd < end, Heading: heading}, nil
}
