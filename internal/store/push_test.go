package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushPreservesIndependentEditAndPortableCursor(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	source := PushSource{Name: "laptop", Ref: "notes.txt", Duplicates: "create"}
	first, outcome, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	require.Equal(t, "added", outcome)
	edited, _, err := s.ReplaceContent(ctx, first.ID, first.Revision, fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	same, outcome, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "skipped", outcome)
	assert.Equal(t, edited.CurrentVersionID, same.CurrentVersionID)
	assert.Equal(t, edited.Revision, same.Revision)
	var snapshot bytes.Buffer
	require.NoError(t, s.ExportMetadata(ctx, &snapshot))
	restored := newTestStore(t)
	require.NoError(t, restored.ImportMetadata(ctx, bytes.NewReader(snapshot.Bytes())))
	require.NoError(t, restored.ValidateMetadata(ctx))
	state, err := restored.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("a1"), state.Hash)
	assert.Equal(t, edited.CurrentVersionID, state.Node.CurrentVersionID)
	changed, outcome, err := restored.AcceptPush(ctx, source, restored.RootID(), "notes.txt", fakeHash("c3"), 5, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "updated", outcome)
	assert.Equal(t, first.ID, changed.ID)
	assert.NotEqual(t, edited.CurrentVersionID, changed.CurrentVersionID)
}

func TestPushPreservesAcceptedCursorAfterPruningBoundVersion(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	source := PushSource{Name: "laptop", Ref: "notes.txt", Duplicates: "create"}
	firstHash := fakeHash("f001")
	acceptedHash := fakeHash("f002")
	manualHash := fakeHash("f003")

	_, _, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", firstHash, 5, "text/plain")
	require.NoError(t, err)
	node, _, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", acceptedHash, 6, "text/plain")
	require.NoError(t, err)
	acceptedVersion := node.CurrentVersionID
	node, _, err = s.ReplaceContent(ctx, node.ID, node.Revision, manualHash, 7, "text/plain")
	require.NoError(t, err)

	_, err = s.PruneContentVersions(ctx, node.ID, node.Revision,
		VersionPruneSelector{VersionIDs: []string{acceptedVersion}}, true)
	require.NoError(t, err)
	_, err = s.ContentVersionByID(ctx, acceptedVersion)
	require.ErrorIs(t, err, ErrNotFound)

	state, err := s.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, acceptedHash, state.Hash, "source cursor survives pruning its bound content version")
	assert.Equal(t, manualHash, state.Node.BlobHash)

	unchanged, outcome, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", acceptedHash, 6, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "skipped", outcome)
	assert.Equal(t, manualHash, unchanged.BlobHash, "unchanged source bytes must preserve a later manual edit")
	assert.NoError(t, s.ValidateMetadata(ctx), "pruned push provenance remains valid backup metadata")
}

func TestPushCursorBackfillsLegacyMetadataFromLatestVersion(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	source := PushSource{Name: "laptop", Ref: "notes.txt", Duplicates: "create"}
	_, _, err := s.AcceptPush(ctx, source, s.RootID(), "notes.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	_, _, err = s.AcceptPush(ctx, source, s.RootID(), "notes.txt", fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	var snapshot bytes.Buffer
	require.NoError(t, s.ExportMetadata(ctx, &snapshot))

	var legacy bytes.Buffer
	for line := range bytes.SplitSeq(snapshot.Bytes(), []byte("\n")) {
		if bytes.Contains(line, []byte(`"type":"push_source"`)) {
			continue
		}
		if len(line) != 0 {
			_, err = legacy.Write(line)
			require.NoError(t, err)
			_, err = legacy.Write([]byte{'\n'})
			require.NoError(t, err)
		}
	}

	restored := newTestStore(t)
	require.NoError(t, restored.ImportMetadata(ctx, bytes.NewReader(legacy.Bytes())))
	require.NoError(t, restored.ValidateMetadata(ctx))
	state, err := restored.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("b2"), state.Hash)
}

func TestPushLinkedSourcesRetainSeparateCursors(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	source := PushSource{Name: "laptop", Ref: "first.txt", Duplicates: "link"}
	first, _, err := s.AcceptPush(ctx, source, s.RootID(), "first.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	secondSource := source
	secondSource.Ref = "second.txt"
	linked, outcome, err := s.AcceptPush(ctx, secondSource, s.RootID(), "second.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "linked", outcome)
	require.Equal(t, first.ID, linked.ID)
	updated, _, err := s.AcceptPush(ctx, source, s.RootID(), "first.txt", fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	unchanged, outcome, err := s.AcceptPush(ctx, secondSource, s.RootID(), "second.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "skipped", outcome)
	assert.Equal(t, updated.CurrentVersionID, unchanged.CurrentVersionID)
	var snapshot bytes.Buffer
	require.NoError(t, s.ExportMetadata(ctx, &snapshot))
	restored := newTestStore(t)
	require.NoError(t, restored.ImportMetadata(ctx, bytes.NewReader(snapshot.Bytes())))
	require.NoError(t, restored.ValidateMetadata(ctx))
	state, err := restored.PushSourceState(ctx, secondSource)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("a1"), state.Hash)
}

func TestPushRollsBackCollisionAndKeepsMonotonicObservation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	source := PushSource{Name: "laptop", Ref: "file.txt", Duplicates: "create"}
	_, _, err := s.AcceptPush(ctx, source, s.RootID(), "file.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	other := source
	other.Name = "other"
	_, _, err = s.AcceptPush(ctx, other, s.RootID(), "file.txt", fakeHash("b2"), 4, "text/plain")
	require.ErrorIs(t, err, ErrExists)
	_, err = s.PushSourceState(ctx, other)
	require.ErrorIs(t, err, ErrNotFound)
	future := time.Now().Add(time.Hour).UTC().Format(timestampLayout)
	source.Ref = "future.txt"
	run, err := s.BeginIngest(ctx, "push", source.Name)
	require.NoError(t, err)
	run.record.StartedAt = future
	first, err := s.IngestFileExact(ctx, run, s.RootID(), "future.txt", fakeHash("a1"), 3, "text/plain", source.Ref, "")
	require.NoError(t, err)
	changed, _, err := s.AcceptPush(ctx, source, s.RootID(), "file.txt", fakeHash("c3"), 5, "text/plain")
	require.NoError(t, err)
	require.Equal(t, first.ID, changed.ID)
	state, err := s.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Greater(t, state.AcceptedAt, future)
	assert.Equal(t, fakeHash("c3"), state.Hash)
}

func TestPushTakesOverRestoredWatchCursorAndPreservesIndependentEdit(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedInitialAuditAuthority(t, s, s.RootID())
	ctx := t.Context()
	run, err := s.BeginIngest(ctx, "watch", "laptop")
	require.NoError(t, err)
	node, err := s.IngestFileExact(ctx, run, s.RootID(), "notes.txt", fakeHash("a1"), 3, "text/plain", "nested/notes.txt", "")
	require.NoError(t, err)
	watched, _, _, err := s.SyncWatchedContent(ctx, "laptop", "nested/notes.txt", fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	edited, _, err := s.ReplaceContent(ctx, watched.ID, watched.Revision, fakeHash("c3"), 5, "text/plain")
	require.NoError(t, err)
	var snapshot bytes.Buffer
	require.NoError(t, s.ExportMetadata(ctx, &snapshot))
	restored := newTestStore(t)
	require.NoError(t, restored.ImportMetadata(ctx, bytes.NewReader(snapshot.Bytes())))
	source := PushSource{Name: "laptop", Ref: "nested/notes.txt", Duplicates: "create"}
	state, err := restored.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("b2"), state.Hash, "take over the watch's last accepted bytes, not the current head")
	assert.Equal(t, edited.CurrentVersionID, state.Node.CurrentVersionID)
	same, outcome, err := restored.AcceptPush(ctx, source, restored.RootID(), "notes.txt", fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "skipped", outcome)
	assert.Equal(t, edited.Revision, same.Revision)
	changed, outcome, err := restored.AcceptPush(ctx, source, restored.RootID(), "notes.txt", fakeHash("d4"), 6, "text/plain")
	require.NoError(t, err)
	assert.Equal(t, "updated", outcome)
	assert.Equal(t, node.ID, changed.ID)
	snapshot.Reset()
	require.NoError(t, restored.ExportMetadata(ctx, &snapshot))
	after := newTestStore(t)
	require.NoError(t, after.ImportMetadata(ctx, bytes.NewReader(snapshot.Bytes())))
	require.NoError(t, after.ValidateMetadata(ctx))
	state, err = after.PushSourceState(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("d4"), state.Hash, "push observations take precedence over the old watch cursor")
	assert.Equal(t, node.ID, state.Node.ID)
}

func TestPushWatchTakeoverRefusesTrash(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	run, err := s.BeginIngest(t.Context(), "watch", "laptop")
	require.NoError(t, err)
	node, err := s.IngestFileExact(t.Context(), run, s.RootID(), "file.txt", fakeHash("a1"), 3, "text/plain", "file.txt", "")
	require.NoError(t, err)
	_, _, err = s.Trash(t.Context(), node.ID, node.Revision)
	require.NoError(t, err)
	source := PushSource{Name: "laptop", Ref: "file.txt", Duplicates: "link"}
	_, err = s.PushSourceState(t.Context(), source)
	require.ErrorIs(t, err, ErrExists)
	_, _, err = s.AcceptPush(t.Context(), source, s.RootID(), "file.txt", fakeHash("b2"), 4, "text/plain")
	require.ErrorIs(t, err, ErrExists)
}

func TestPushAuditReplaysCreateLinkAndUpdate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedInitialAuditAuthority(t, s, s.RootID())
	source := PushSource{Name: "laptop", Ref: "first.txt", Duplicates: "link"}
	first, _, err := s.AcceptPush(t.Context(), source, s.RootID(), "first.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	source.Ref = "linked.txt"
	linked, _, err := s.AcceptPush(t.Context(), source, s.RootID(), "linked.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	require.Equal(t, first.ID, linked.ID)
	_, _, err = s.AcceptPush(t.Context(), source, s.RootID(), "linked.txt", fakeHash("b2"), 4, "text/plain")
	require.NoError(t, err)
	require.NoError(t, s.ValidateMetadata(t.Context()))
	var snapshot bytes.Buffer
	require.NoError(t, s.ExportMetadata(t.Context(), &snapshot))
	restored := newTestStore(t)
	require.NoError(t, restored.ImportMetadata(t.Context(), bytes.NewReader(snapshot.Bytes())))
	require.NoError(t, restored.ValidateMetadata(t.Context()))
	state, err := restored.PushSourceState(t.Context(), source)
	require.NoError(t, err)
	assert.Equal(t, fakeHash("b2"), state.Hash)
}

func TestPushMappedTrashIsAnErrorAndNotAnUnknownSource(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	source := PushSource{Name: "laptop", Ref: "file.txt", Duplicates: "link"}
	node, _, err := s.AcceptPush(t.Context(), source, s.RootID(), "file.txt", fakeHash("a1"), 3, "text/plain")
	require.NoError(t, err)
	_, _, err = s.Trash(t.Context(), node.ID, node.Revision)
	require.NoError(t, err)
	_, err = s.PushSourceState(t.Context(), source)
	require.ErrorIs(t, err, ErrExists)
	_, _, err = s.AcceptPush(t.Context(), source, s.RootID(), "file.txt", fakeHash("b2"), 4, "text/plain")
	require.ErrorIs(t, err, ErrExists)
}

func TestPushConcurrentRetriesResolveOneNode(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	source := PushSource{Name: "laptop", Ref: "file.txt", Duplicates: "create"}
	type result struct {
		node Node
		err  error
	}
	outcomes := make(chan result, 8)
	for range 8 {
		go func() {
			node, _, err := s.AcceptPush(t.Context(), source, s.RootID(), "file.txt", fakeHash("a1"), 3, "text/plain")
			outcomes <- result{node: node, err: err}
		}()
	}
	var nodeID int64
	for range 8 {
		outcome := <-outcomes
		require.NoError(t, outcome.err)
		if nodeID == 0 {
			nodeID = outcome.node.ID
		}
		assert.Equal(t, nodeID, outcome.node.ID)
	}
	state, err := s.PushSourceState(t.Context(), source)
	require.NoError(t, err)
	assert.Equal(t, nodeID, state.Node.ID)
}
