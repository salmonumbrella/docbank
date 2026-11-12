package api_test

import (
	"fmt"
	"net/http"
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
