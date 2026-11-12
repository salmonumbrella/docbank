package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"cloud.google.com/go/civil"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
)

func TestTimelineHydrationEmpty(t *testing.T) {
	s := newTestStore(t)
	err := s.withReadTx(t.Context(), func(tx *sql.Tx) error {
		items, err := s.hydrateTimelineItems(t.Context(), tx, nil, false)
		require.NoError(t, err)
		require.Empty(t, items)
		return nil
	})
	require.NoError(t, err)
}

func checkTimelineHydrationCurrentPeople(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "people.txt", fakeHash("dc1"), 1, "text/plain")
	require.NoError(t, err)
	target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
	record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "dc2")
	primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
	require.True(t, ok)
	record.Primaries = append(record.Primaries, primary)
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	input, err := s.PrepareDocumentPeopleInputs(ctx, node.CurrentVersionID)
	require.NoError(t, err)
	personID := input.Bindings["email:ada@example.test"][0].PersonID
	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: personID, Role: "author", Action: "assert", Revision: 1})
	require.NoError(t, err)
	person, _, err := s.PersonByID(ctx, personID)
	require.NoError(t, err)
	_, err = s.LinkExternalIdentity(ctx, PersonExternalIdentity{PersonID: personID, System: "msgvault", ArchiveID: "synthetic", UID: "person-a", UIDKind: "vcard_uid", UIDState: "current"}, person.Revision)
	require.NoError(t, err)
	blind, err := s.CreatePerson(ctx, "Hidden recipient", "operator")
	require.NoError(t, err)
	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: blind.PersonID, Role: "blind_copy", Action: "assert", Revision: 1})
	require.NoError(t, err)
	input, err = s.PrepareDocumentPeopleInputs(ctx, node.CurrentVersionID)
	require.NoError(t, err)
	_, err = s.PublishDocumentPeople(ctx, documentPeoplePublicationForInput(t, input))
	require.NoError(t, err)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 10}
	page, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, page.DateOnly, 1)
	require.Len(t, page.DateOnly[0].People, 1)
	got := page.DateOnly[0].People[0]
	require.Equal(t, personID, got.PersonID)
	require.Equal(t, []string{"author"}, got.Roles)
	require.True(t, got.LinkedContact)
	require.Equal(t, "synthetic", got.ArchiveID)
	require.Equal(t, "person-a", got.UID)
	if s.postgresSchema == "" {
		for _, tc := range []struct {
			text string
			want int
		}{
			{"person:" + personID, 1},
			{"person:" + blind.PersonID, 0},
			{"NOT person:" + blind.PersonID, 1},
			{"NOT person:" + personID, 0},
		} {
			scoped := req
			scoped.Scope.Query.Syntax = "advanced"
			scoped.Scope.Query.Text = tc.text
			page, err := svc.TimelineDay(ctx, "owner", scoped)
			require.NoError(t, err, tc.text)
			require.Len(t, page.DateOnly, tc.want, tc.text)
		}
	}

	for _, state := range []string{"retired", "unlinked"} {
		person, _, err = s.PersonByID(ctx, personID)
		require.NoError(t, err)
		_, err = s.LinkExternalIdentity(ctx, PersonExternalIdentity{PersonID: personID, System: "msgvault", ArchiveID: "synthetic", UID: "person-a", UIDKind: "vcard_uid", UIDState: state}, person.Revision)
		require.NoError(t, err)
		page, err = svc.TimelineDay(ctx, "owner", req)
		require.NoError(t, err)
		require.Len(t, page.DateOnly, 1)
		require.Len(t, page.DateOnly[0].People, 1)
		require.False(t, page.DateOnly[0].People[0].LinkedContact)
		require.Empty(t, page.DateOnly[0].People[0].UID)
	}

	// Suppression advances the binding epoch and excludes the stale derived edge.
	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: personID, Role: "author", Action: "suppress", Revision: 1})
	require.NoError(t, err)
	page, err = svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, page.DateOnly, 1)
	require.Empty(t, page.DateOnly[0].People)
}

// Candidate evidence must match a current, visible actor on each placement.
func checkTimelineHydrationCandidateEvidence(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	var versions []string
	for i, name := range []string{"first.txt", "second.txt"} {
		node, err := s.CreateFile(ctx, s.RootID(), name, fakeHash([]string{"dc3", "dc4"}[i]), 1, "text/plain")
		require.NoError(t, err)
		versions = append(versions, node.CurrentVersionID)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, "dc2")
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
	}
	openCandidateForTest(t, s, candidateForTest(t, "email:ada@example.test", "Ada", "", []PersonCandidateOccurrence{
		{ContentVersionID: versions[0], Role: "author", EvidenceKind: "source_metadata", EvidenceID: "metadata/dc2"},
		{ContentVersionID: versions[1], Role: "author", EvidenceKind: "source_metadata", EvidenceID: "metadata/dc2"},
	}))
	openCandidateForTest(t, s, candidateForTest(t, "email:ada@example.test", "Ada", "", []PersonCandidateOccurrence{
		{ContentVersionID: versions[0], Role: "author", EvidenceKind: "source_metadata", EvidenceID: "metadata/other"},
	}))
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	page, err := svc.TimelineDay(ctx, "owner", TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.DateOnly, 2)
	for _, item := range page.DateOnly {
		require.Equal(t, 1, item.UnresolvedCount)
	}
}

// Count statements and their real bind arguments at the hydration boundary.
type timelineCountingQuerier struct {
	metadataQuerier
	statements, maxArgs int
}

func (q *timelineCountingQuerier) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	q.statements++
	q.maxArgs = max(q.maxArgs, len(args))
	return q.metadataQuerier.QueryContext(ctx, statement, args...)
}

func checkTimelineHydrationChunking(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	placements := make([]TimelinePlacement, 0, 401)
	for i := range 401 {
		seed := fmt.Sprintf("%x", 0x1000+i)
		node, err := s.CreateFile(ctx, s.RootID(), "chunk-"+seed+".txt", fakeHash(seed), 1, "text/plain")
		require.NoError(t, err)
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		address := fmt.Sprintf("person-%03d@example.test", i)
		actor := &record.Events[0].Actors[0]
		actor.Address, actor.ActorKey = address, "email:"+address
		actor.Claim = fmt.Sprintf(`{"address":%q}`, address)
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
		_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
		placements = append(placements, TimelinePlacement{NodeID: node.ID, VersionID: node.CurrentVersionID, EventID: record.Events[0].EventID})
	}
	for _, tc := range []struct{ count, statements int }{{1, 3}, {401, 6}} {
		err := s.withReadTx(ctx, func(tx *sql.Tx) error {
			q := &timelineCountingQuerier{metadataQuerier: tx}
			items, err := s.hydrateTimelineItems(ctx, q, placements[:tc.count], false)
			require.NoError(t, err)
			require.Len(t, items, tc.count)
			require.Equal(t, tc.statements, q.statements)
			require.LessOrEqual(t, q.maxArgs, 500)
			for _, item := range items {
				require.NotEmpty(t, item.Name)
			}
			return nil
		})
		require.NoError(t, err)
	}
}
