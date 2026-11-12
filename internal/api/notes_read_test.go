package api_test

import (
	"encoding/json/v2"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoteTextReadRetainedOriginal(t *testing.T) {
	t.Parallel()
	ts, s := newTestServer(t, nil)
	ctx := t.Context()
	const original = "# Before\r\nold\r\n"
	hash, size, err := s.Blobs.WriteContext(ctx, strings.NewReader(original))
	require.NoError(t, err)
	n, err := s.CreateFile(ctx, s.RootID(), "old.md", hash, size, "text/markdown")
	require.NoError(t, err)
	newHash, newSize, err := s.Blobs.WriteContext(ctx, strings.NewReader("# After\nnew\n"))
	require.NoError(t, err)
	_, _, err = s.ReplaceContent(ctx, n.ID, n.Revision, newHash, newSize, "text/markdown")
	require.NoError(t, err)
	url := "/api/v1/notes/" + strconv.FormatInt(n.ID, 10) + "/text?version_id=" + n.CurrentVersionID
	resp, body := get(t, ts, url, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result struct {
		Text             string `json:"text"`
		ContentVersionID string `json:"content_version_id"`
		SHA256           string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	require.Equal(t, original, result.Text)
	require.Equal(t, n.CurrentVersionID, result.ContentVersionID)
	require.Equal(t, hash, result.SHA256)
	other, err := s.CreateFile(ctx, s.RootID(), "other.md", hash, size, "text/markdown")
	require.NoError(t, err)
	resp, body = get(t, ts, "/api/v1/notes/"+strconv.FormatInt(other.ID, 10)+"/text?version_id="+n.CurrentVersionID, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)
}

func TestNoteOutlineSectionEndsAreInclusive(t *testing.T) {
	t.Parallel()
	ts, s := newTestServer(t, nil)
	ctx := t.Context()
	const original = "# First\r\none\r\n# Second\r\ntwo\r\n"
	hash, size, err := s.Blobs.WriteContext(ctx, strings.NewReader(original))
	require.NoError(t, err)
	n, err := s.CreateFile(ctx, s.RootID(), "note.md", hash, size, "text/markdown")
	require.NoError(t, err)
	base := "/api/v1/notes/" + strconv.FormatInt(n.ID, 10)
	resp, body := get(t, ts, base+"/outline", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var outline struct {
		Headings []struct {
			LineStart int `json:"line_start"`
			LineEnd   int `json:"line_end"`
		} `json:"headings"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &outline))
	require.Len(t, outline.Headings, 2)
	require.Equal(t, 1, outline.Headings[0].LineStart)
	require.Equal(t, 2, outline.Headings[0].LineEnd)
	resp, body = get(t, ts, base+"/text?line_start=1&line_end=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	require.Equal(t, "# First\r\none\r\n", result.Text)
}
