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
