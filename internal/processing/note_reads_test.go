package processing

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document/notemarkdown"
)

func TestSelectNoteTextOriginalSectionsAndUTF8(t *testing.T) {
	t.Parallel()
	const src = "\ufeff# First\r\nα🙂\r\n## Child\r\ninside\r\n# Next\r\nend\r\n"
	note, err := notemarkdown.Parse(t.Context(), []byte(src), notemarkdown.Limits{})
	require.NoError(t, err)
	section, err := SelectNoteText([]byte(src), note, NoteTextOptions{Heading: "First"})
	require.NoError(t, err)
	require.Equal(t, "# First\r\nα🙂\r\n## Child\r\ninside\r\n", section.Text)
	require.Equal(t, 3, section.Start)
	require.False(t, section.Truncated)
	lines, err := SelectNoteText([]byte(src), note, NoteTextOptions{LineStart: 1, LineEnd: 4})
	require.NoError(t, err)
	require.Equal(t, "\ufeff"+section.Text, lines.Text)
	require.Equal(t, 4, lines.LineEnd)
	first, err := SelectNoteText([]byte(src), note, NoteTextOptions{Offset: new(12), MaxBytes: 2})
	require.NoError(t, err)
	require.Equal(t, "α", first.Text)
	require.Equal(t, 14, first.End)
	require.True(t, first.Truncated)
	_, err = SelectNoteText([]byte(src), note, NoteTextOptions{Offset: new(14), MaxBytes: 1})
	require.ErrorIs(t, err, ErrNoteRange)
	_, err = SelectNoteText([]byte(src), note, NoteTextOptions{Offset: new(13)})
	require.ErrorIs(t, err, ErrNoteRange)
}

func TestSelectNoteTextLineBoundariesAndPages(t *testing.T) {
	t.Parallel()
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		src := []byte("\ufeff# One" + eol + "α🙂" + eol + "# Two" + eol + "last")
		note, err := notemarkdown.Parse(t.Context(), src, notemarkdown.Limits{})
		require.NoError(t, err)
		first := note.Headings[0]
		section, err := SelectNoteText(src, note, NoteTextOptions{Heading: first.Path})
		require.NoError(t, err)
		require.Equal(t, string(src[first.Start:first.SectionEnd]), section.Text)
		lines, err := SelectNoteText(src, note, NoteTextOptions{LineStart: first.Line, LineEnd: first.SectionEndLine - 1})
		require.NoError(t, err)
		require.Equal(t, "\ufeff"+section.Text, lines.Text)
		var joined string
		offset := 0
		for {
			page, err := SelectNoteText(src, note, NoteTextOptions{Offset: &offset, MaxBytes: 4})
			require.NoError(t, err)
			require.LessOrEqual(t, len(page.Text), 4)
			joined += page.Text
			if !page.Truncated {
				break
			}
			require.Greater(t, page.End, offset)
			offset = page.End
		}
		require.Equal(t, string(src), joined)
		empty, err := SelectNoteText(src, note, NoteTextOptions{Offset: new(len(src))})
		require.NoError(t, err)
		require.Empty(t, empty.Text)
		require.False(t, empty.Truncated)
	}
}

func TestSelectNoteTextHeadingAmbiguity(t *testing.T) {
	t.Parallel()
	src := []byte("# Parent A\n## Repeat\nfirst\n# Parent B\n## Repeat\nsecond\n")
	note, err := notemarkdown.Parse(t.Context(), src, notemarkdown.Limits{})
	require.NoError(t, err)
	_, err = SelectNoteText(src, note, NoteTextOptions{Heading: "Repeat"})
	var ambiguous *NoteHeadingAmbiguous
	require.ErrorAs(t, err, &ambiguous)
	require.Equal(t, []string{"Parent A > Repeat", "Parent B > Repeat"}, ambiguous.Candidates)
	selected, err := SelectNoteText(src, note, NoteTextOptions{Heading: "Parent B > Repeat"})
	require.NoError(t, err)
	require.Equal(t, "## Repeat\nsecond\n", selected.Text)
	_, err = SelectNoteText(src, note, NoteTextOptions{Heading: "absent"})
	require.ErrorIs(t, err, ErrNoteHeadingNotFound)
	_, err = SelectNoteText(src, note, NoteTextOptions{Heading: "Repeat", LineStart: 1})
	require.ErrorIs(t, err, ErrNoteRange)
}
