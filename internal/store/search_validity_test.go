package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadSearchValidityFence(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	a, err := s.CreateFile(ctx, s.RootID(), "old", fakeHash("a"), 1, "text/plain")
	require.NoError(t, err)
	b, err := s.CreateFile(ctx, s.RootID(), "new", fakeHash("b"), 1, "text/plain")
	require.NoError(t, err)
	_, err = s.PutSupersession(ctx, b.ID, a.ID, a.Revision, "")
	require.NoError(t, err)
	graph, err := s.LoadSearchValidity(ctx, []string{a.CurrentVersionID})
	require.NoError(t, err)
	require.Len(t, graph, 1)
	require.Empty(t, graph[a.ID].Successors)
	require.False(t, graph[a.ID].Declared)
	graph, err = s.LoadSearchValidity(ctx, []string{a.CurrentVersionID, b.CurrentVersionID})
	require.NoError(t, err)
	require.Equal(t, []int64{b.ID}, graph[a.ID].Successors)
	require.True(t, graph[a.ID].Declared)
	require.True(t, graph[b.ID].Declared)
	require.Equal(t, "/new", graph[b.ID].Path)
	_, err = s.db.Exec(`INSERT INTO node_supersessions VALUES(?,?,?,?)`, a.ID, b.ID, "", nowRFC3339())
	require.NoError(t, err)
	_, err = s.LoadSearchValidity(ctx, []string{a.CurrentVersionID, b.CurrentVersionID})
	require.ErrorIs(t, err, ErrInvalidSupersession)
	// Corrupt authority outside the fence cannot affect the permitted document.
	_, err = s.LoadSearchValidity(ctx, []string{a.CurrentVersionID})
	require.NoError(t, err)
}

func TestLoadSearchValidityRejectsMalformedDates(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	n, err := s.CreateFile(ctx, s.RootID(), "dated", fakeHash("a"), 1, "text/plain")
	require.NoError(t, err)
	for _, dates := range [][2]string{{"", "2027-01-01"}, {"2025-01-01", ""}, {"2025-02-30", "2027-01-01"}} {
		_, err = s.db.Exec(`INSERT OR REPLACE INTO node_effective_windows(node_id,effective_from,effective_to) VALUES(?,?,?)`, n.ID, dates[0], dates[1])
		require.NoError(t, err)
		_, err = s.LoadSearchValidity(ctx, []string{n.CurrentVersionID})
		require.ErrorIs(t, err, ErrInvalidSupersession)
	}
}

func TestLoadSearchValidityCurrentLiveVersions(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	a, err := s.CreateFile(ctx, s.RootID(), "old", fakeHash("a"), 1, "text/plain")
	require.NoError(t, err)
	b, err := s.CreateFile(ctx, s.RootID(), "new", fakeHash("b"), 1, "text/plain")
	require.NoError(t, err)
	_, err = s.PutSupersession(ctx, b.ID, a.ID, a.Revision, "")
	require.NoError(t, err)
	_, _, err = s.Trash(ctx, b.ID, b.Revision)
	require.NoError(t, err)
	graph, err := s.LoadSearchValidity(ctx, []string{a.CurrentVersionID, b.CurrentVersionID})
	require.NoError(t, err)
	require.Len(t, graph, 1)
	require.Empty(t, graph[a.ID].Successors)
	oldVersion := a.CurrentVersionID
	a, err = s.NodeByID(ctx, a.ID)
	require.NoError(t, err)
	_, _, err = s.ReplaceContent(ctx, a.ID, a.Revision, fakeHash("c"), 1, "text/plain")
	require.NoError(t, err)
	graph, err = s.LoadSearchValidity(ctx, []string{oldVersion})
	require.NoError(t, err)
	require.Empty(t, graph)
	_, err = s.LoadSearchValidity(ctx, make([]string, 4097))
	require.ErrorIs(t, err, ErrInvalidSupersession)
}

func TestLoadSearchValidityDegreeAndLongChain(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	var nodes []Node
	var ids []string
	for i := range 18 {
		n, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("file-%d", i), fakeHash(fmt.Sprintf("%x", i+100)), 1, "text/plain")
		require.NoError(t, err)
		nodes = append(nodes, n)
		ids = append(ids, n.CurrentVersionID)
	}
	for i := 1; i < len(nodes); i++ {
		_, err := s.db.Exec(`INSERT INTO node_supersessions VALUES(?,?,?,?)`, nodes[i].ID, nodes[0].ID, "", nowRFC3339())
		require.NoError(t, err)
	}
	_, err := s.LoadSearchValidity(ctx, ids)
	require.ErrorIs(t, err, ErrInvalidSupersession)
	_, err = s.db.Exec(`DELETE FROM node_supersessions`)
	require.NoError(t, err)
	for i := 1; i < len(nodes); i++ {
		_, err = s.db.Exec(`INSERT INTO node_supersessions VALUES(?,?,?,?)`, nodes[i].ID, nodes[i-1].ID, "", nowRFC3339())
		require.NoError(t, err)
	}
	_, err = s.LoadSearchValidity(ctx, ids)
	require.NoError(t, err)
}
