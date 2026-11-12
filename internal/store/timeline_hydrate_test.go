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
	_, err = s.PublishDocumentPeople(ctx, documentPeoplePublicationForInput(t, input))
	require.NoError(t, err)
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 10}
	provisional, err := svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, provisional.DateOnly, 1)
	require.Len(t, provisional.DateOnly[0].People, 1)
	require.Equal(t, personID, provisional.DateOnly[0].People[0].PersonID)
	require.Equal(t, "Ada", provisional.DateOnly[0].People[0].DisplayName)
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
		savedPayload := fmt.Appendf(nil, `{"syntax":"advanced","text":%q}`, "NOT person:"+blind.PersonID)
		_, err = s.CreateSavedQuery(ctx, "Safe negative", "", SavedQueryKindQuery, savedPayload)
		require.NoError(t, err)
		for _, q := range []string{
			fmt.Sprintf(`{"filters":{"person_ids":[%q]}}`, personID),
			`{"syntax":"advanced","text":"saved:\"Safe negative\""}`,
		} {
			scoped := req
			scoped.Scope.Query = snapshotTestQuery(t, q)
			days, err := svc.TimelineDays(ctx, "owner", scoped.Scope, date, date)
			require.NoError(t, err)
			require.EqualValues(t, 1, days.Days[0].Count)
			page, err := svc.TimelineDay(ctx, "owner", scoped)
			require.NoError(t, err)
			require.Len(t, page.DateOnly, 1)
			require.Equal(t, node.ID, page.DateOnly[0].NodeID)
		}
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

	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: personID, Role: "speaker", Action: "assert", Revision: 1})
	require.NoError(t, err)
	survivor, err := s.CreatePerson(ctx, "Merged survivor", "operator")
	require.NoError(t, err)
	_, err = s.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: node.CurrentVersionID, PersonID: survivor.PersonID, Role: "author", Action: "assert", Revision: 1})
	require.NoError(t, err)
	person, _, err = s.PersonByID(ctx, personID)
	require.NoError(t, err)
	survivor, _, err = s.PersonByID(ctx, survivor.PersonID)
	require.NoError(t, err)
	_, err = s.MergePersons(ctx, survivor.PersonID, personID, "90000000-0000-4000-8000-000000000045", survivor.Revision, person.Revision)
	require.NoError(t, err)
	page, err = svc.TimelineDay(ctx, "owner", req)
	require.NoError(t, err)
	require.Len(t, page.DateOnly, 1)
	require.Len(t, page.DateOnly[0].People, 1)
	require.Equal(t, survivor.PersonID, page.DateOnly[0].People[0].PersonID)
	require.Equal(t, "Merged survivor", page.DateOnly[0].People[0].DisplayName)
	require.ElementsMatch(t, []string{"author", "speaker"}, page.DateOnly[0].People[0].Roles)
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
		for j, privacy := range []string{"actor", "event"} {
			event := record.Events[0]
			event.Actors = append([]document.DocumentEventActorV1(nil), event.Actors...)
			address := privacy + "@example.test"
			event.Actors[0].Address, event.Actors[0].ActorKey = address, "email:"+address
			event.Actors[0].Claim = fmt.Sprintf(`{"address":%q}`, address)
			event.Actors[0].DisplayName = privacy
			event.Actors[0].Sensitive = privacy == "actor"
			event.Sensitive = privacy == "event"
			event.SourceKey = fmt.Sprintf("metadata/dc2/private-%d", j)
			event.EventID = document.DocumentEventID(record.VaultUID, record.ContentVersionID, event.SourceKey, event.DateKind, event.DateValue, event.Precision, event.TimezoneKind, event.EvidenceSHA256)
			record.Events = append(record.Events, event)
		}
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
	for _, privacy := range []string{"actor", "event"} {
		openCandidateForTest(t, s, candidateForTest(t, "email:"+privacy+"@example.test", privacy, "", []PersonCandidateOccurrence{
			{ContentVersionID: versions[0], Role: "author", EvidenceKind: "source_metadata", EvidenceID: "metadata/dc2"},
			{ContentVersionID: versions[1], Role: "author", EvidenceKind: "source_metadata", EvidenceID: "metadata/dc2"},
		}))
	}
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
	for _, tc := range []struct{ count, statements int }{{1, 4}, {401, 8}} {
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

func checkTimelineHydrationEmailFamily(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	f := newEmailFixture(t, s, "family-parent.jpg")
	original, err := s.NodeByPath(ctx, "/family-parent.jpg")
	require.NoError(t, err)
	imageParent, version, err := s.ReplaceContent(ctx, original.ID, original.Revision, original.BlobHash, original.Size, "image/jpeg")
	require.NoError(t, err)
	f.publication.ContentVersionID = version.ID
	view, err := s.PublishEmailGeneration(ctx, f.publication)
	require.NoError(t, err)
	first, err := s.PublishEmailDocuments(ctx, attachmentRequest(t, s, view, "timeline-family-first"))
	require.NoError(t, err)
	child := first.Relations[0].Child
	require.NotNil(t, child)
	target := requireDocumentEventTarget(t, s, child.VersionID)
	record := documentEventRecord(t, s.VaultID(), child.VersionID, "dc6")
	primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
	require.True(t, ok)
	record.Primaries = append(record.Primaries, primary)
	_, err = s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
	require.NoError(t, err)
	placement := TimelinePlacement{NodeID: child.NodeID, VersionID: child.VersionID, EventID: record.Events[0].EventID}
	parentOf := func(hideLocked bool) *int64 {
		t.Helper()
		var parent *int64
		err := s.withReadTx(ctx, func(tx *sql.Tx) error {
			items, err := s.hydrateTimelineItems(ctx, tx, []TimelinePlacement{placement}, hideLocked)
			if err != nil {
				return err
			}
			require.Len(t, items, 1)
			parent = items[0].FamilyParentNodeID
			return nil
		})
		require.NoError(t, err)
		return parent
	}
	parent := parentOf(true)
	require.NotNil(t, parent)
	require.Equal(t, view.Version.NodeID, *parent)
	asset, err := s.PhotoAssetForNode(ctx, imageParent.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetupPhotoHidden(ctx, "synthetic-passcode"))
	asset, err = s.SetPhotoAssetHidden(ctx, asset.ID, asset.Revision, true)
	require.NoError(t, err)
	require.Nil(t, parentOf(true), "a locked parent is not disclosed")
	visibleParent := parentOf(false)
	require.NotNil(t, visibleParent)
	require.Equal(t, imageParent.ID, *visibleParent)
	token, _, err := s.UnlockPhotoHidden(ctx, "synthetic-passcode")
	require.NoError(t, err)
	_, err = s.SetPhotoAssetHidden(WithPhotoHiddenToken(ctx, token), asset.ID, asset.Revision, false)
	require.NoError(t, err)
	childNode, err := s.NodeByID(ctx, child.NodeID)
	require.NoError(t, err)
	other, err := s.CreateFile(ctx, s.RootID(), "other-family.eml", view.Version.BlobHash, view.Version.Size, "message/rfc822")
	require.NoError(t, err)
	otherPublication := f.publication
	otherPublication.ContentVersionID = other.CurrentVersionID
	otherView, err := s.PublishEmailGeneration(ctx, otherPublication)
	require.NoError(t, err)
	request := attachmentRequest(t, s, otherView, "timeline-family-shared")
	request.Reuse = []document.EmailDocumentReuse{{PartPath: "1.2", Child: *child, Revision: childNode.Revision}}
	_, err = s.PublishEmailDocuments(ctx, request)
	require.NoError(t, err)
	require.Nil(t, parentOf(true), "a shared attachment has no unique scalar parent")
	_, _, err = s.Trash(ctx, other.ID, UnconditionalRev)
	require.NoError(t, err)
	parent = parentOf(true)
	require.NotNil(t, parent)
	require.Equal(t, view.Version.NodeID, *parent)
	_, _, err = s.ReplaceContent(ctx, view.Version.NodeID, UnconditionalRev, fakeHash("dc7"), 1, "message/rfc822")
	require.NoError(t, err)
	require.Nil(t, parentOf(true), "historical and trashed parents do not supply grouping")
}

func checkTimelineHydrationStalePeopleHeads(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	node, err := s.CreateFile(ctx, s.RootID(), "stale-people.txt", fakeHash("df1"), 1, "text/plain")
	require.NoError(t, err)
	publishEvent := func(seed string) {
		t.Helper()
		target := requireDocumentEventTarget(t, s, node.CurrentVersionID)
		record := documentEventRecord(t, s.VaultID(), node.CurrentVersionID, seed)
		primary, ok := document.SelectPrimaryEvent(record.Events, record.DocumentKind, "vault", "safe")
		require.True(t, ok)
		record.Primaries = append(record.Primaries, primary)
		_, err := s.PublishDocumentEvents(ctx, target, DocumentEventsDeriverFingerprint, requireDocumentEventInputsSHA256(t, s, target), mustMarshalDocumentEvents(t, record))
		require.NoError(t, err)
	}
	publishPeople := func() {
		t.Helper()
		input, err := s.PrepareDocumentPeopleInputs(ctx, node.CurrentVersionID)
		require.NoError(t, err)
		_, err = s.PublishDocumentPeople(ctx, documentPeoplePublicationForInput(t, input))
		require.NoError(t, err)
	}
	publishEvent("df2")
	publishPeople()
	svc := NewQuerySnapshotService(s)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	date, err := civil.ParseDate("2019-03-04")
	require.NoError(t, err)
	req := TimelineDayRequest{Scope: TimelineScope{Query: snapshotTestQuery(t, `{}`), Zone: "UTC"}, Date: date, Limit: 10}
	check := func(want int) {
		t.Helper()
		page, err := svc.TimelineDay(ctx, "owner", req)
		require.NoError(t, err)
		require.Len(t, page.DateOnly, 1)
		require.Len(t, page.DateOnly[0].People, want)
	}
	check(1)
	_, _, err = s.Move(ctx, node.ID, s.RootID(), "renamed-people.txt", node.Revision)
	require.NoError(t, err)
	check(0) // The node revision no longer matches the people head.
	publishPeople()
	check(1)
	publishEvent("df3")
	check(0) // A new event generation invalidates the old people head.
	publishPeople()
	check(1)
	input, err := s.PrepareDocumentPeopleInputs(ctx, node.CurrentVersionID)
	require.NoError(t, err)
	oldResolver := documentPeoplePublicationForInput(t, input)
	oldResolver.ResolverFingerprint = fakeHash("df4")
	_, err = s.PublishDocumentPeople(ctx, oldResolver)
	require.NoError(t, err)
	check(0) // An otherwise current head from an older resolver is not safe to hydrate.
	publishPeople()
	check(1)
	_, err = s.RebuildDocumentPeople(ctx, "90000000-0000-4000-8000-000000000046")
	require.NoError(t, err)
	check(0) // Resolver rebuild advances the production binding fence.
	publishPeople()
	check(1)
}
