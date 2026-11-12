package api_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
)

func TestVersionPeopleWriteAtomic(t *testing.T) {
	t.Parallel()
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "synthetic.txt", "synthetic")
	a, err := s.CreatePerson(t.Context(), "Synthetic A", "operator")
	require.NoError(t, err)
	b, err := s.CreatePerson(t.Context(), "Synthetic B", "operator")
	require.NoError(t, err)
	path := fmt.Sprintf("/api/v1/versions/%s/people/assertions", node.CurrentVersionID)
	change := func(id string, rev int64) api.VersionPersonAssertionChange {
		return api.VersionPersonAssertionChange{PersonID: id, Role: "author", Action: "assert", Revision: rev}
	}
	res, body := do(t, ts, http.MethodPut, path, nil, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change(a.PersonID, 1), change(b.PersonID, 1)}})
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.Contains(t, body, `"undo"`)
	require.Contains(t, body, `"withdrawn"`)
	res, body = do(t, ts, http.MethodPut, path, nil, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change(a.PersonID, 1)}})
	require.Equal(t, http.StatusOK, res.StatusCode, body) // revision 1 may address an existing first assertion
	res, body = do(t, ts, http.MethodPut, path, nil, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change(b.PersonID, 1), change(a.PersonID, 1)}})
	require.Equal(t, http.StatusConflict, res.StatusCode, body)
	require.Equal(t, "stale_revision", decodeProblem(t, body).Code)
	rows, err := s.DocumentPersonAssertions(t.Context(), node.CurrentVersionID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.PersonID == b.PersonID {
			require.Equal(t, int64(1), row.Revision)
		}
	}
}

func TestVersionPeopleWriteBlindCopyDisclosure(t *testing.T) {
	t.Parallel()
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "synthetic-bcc.txt", "synthetic")
	person, err := s.CreatePerson(t.Context(), "Synthetic Blind Recipient", "operator")
	require.NoError(t, err)
	path := fmt.Sprintf("/api/v1/versions/%s/people/assertions", node.CurrentVersionID)
	change := api.VersionPersonAssertionChange{PersonID: person.PersonID, Role: "blind_copy", Action: "assert", Revision: 1}
	res, body := do(t, ts, http.MethodPost, "/api/daemon/web-session", nil, nil)
	require.Equal(t, http.StatusCreated, res.StatusCode, body)
	var session struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &session))
	headers := map[string]string{"X-Api-Key": "", api.WebSessionHeader: session.Token}
	res, body = do(t, ts, http.MethodPut, path, headers, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change}})
	require.Equal(t, http.StatusUnprocessableEntity, res.StatusCode, body)
	require.Equal(t, "role_forbidden", decodeProblem(t, body).Code)
	rows, err := s.DocumentPersonAssertions(t.Context(), node.CurrentVersionID)
	require.NoError(t, err)
	require.Empty(t, rows)
	res, body = do(t, ts, http.MethodPut, path, nil, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change}})
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.NotContains(t, body, person.PersonID)
	require.NotContains(t, body, "blind_copy")
	var result api.VersionPeopleWrite
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	require.Empty(t, result.Undo)
	require.Empty(t, result.Assertions)
	rows, err = s.DocumentPersonAssertions(t.Context(), node.CurrentVersionID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "blind_copy", rows[0].Role)
	res, body = do(t, ts, http.MethodPut, path+"?disclosure=full", headers, api.PutVersionPersonAssertionsRequest{Changes: []api.VersionPersonAssertionChange{change}})
	require.Equal(t, http.StatusForbidden, res.StatusCode, body)
}

func TestVersionPeopleWriteRejectsInvalidBatch(t *testing.T) {
	t.Parallel()
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "synthetic-invalid.txt", "synthetic")
	person, err := s.CreatePerson(t.Context(), "Synthetic Author", "operator")
	require.NoError(t, err)
	path := fmt.Sprintf("/api/v1/versions/%s/people/assertions", node.CurrentVersionID)
	valid := api.VersionPersonAssertionChange{PersonID: person.PersonID, Role: "author", Action: "assert", Revision: 1}
	tooMany := make([]api.VersionPersonAssertionChange, 33)
	for i := range tooMany {
		tooMany[i] = valid
	}
	longNote := valid
	longNote.Note = strings.Repeat("x", 1001)
	depicted := valid
	depicted.Role = "depicted"
	for _, changes := range [][]api.VersionPersonAssertionChange{tooMany, {longNote}, {depicted}} {
		res, body := do(t, ts, http.MethodPut, path, nil, api.PutVersionPersonAssertionsRequest{Changes: changes})
		require.Equal(t, http.StatusUnprocessableEntity, res.StatusCode, body)
	}
	rows, err := s.DocumentPersonAssertions(t.Context(), node.CurrentVersionID)
	require.NoError(t, err)
	require.Empty(t, rows)
}
