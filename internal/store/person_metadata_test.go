package store

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/canonical"
)

func TestPersonAuthorityMetadataRoundTrip(t *testing.T) {
	source := newTestStore(t)
	ctx := t.Context()
	person, err := source.CreatePerson(ctx, "Ada Lovelace", "operator")
	require.NoError(t, err)
	identity, err := source.AddPersonIdentity(ctx, person.PersonID, person.Revision, PersonIdentity{
		Kind: "email", ValueDisplay: "ada@example.test", Origin: "operator",
		EvidenceKind: "operator_assertion", EvidenceID: "synthetic", Confidence: "operator_asserted",
	})
	require.NoError(t, err)
	person, _, err = source.PersonByID(ctx, person.PersonID)
	require.NoError(t, err)
	require.NoError(t, source.RecordExternalUIDAliases(ctx, "msgvault", "synthetic", "current", []string{"retired"}))
	_, err = source.LinkExternalIdentity(ctx, PersonExternalIdentity{PersonID: person.PersonID,
		System: "msgvault", ArchiveID: "synthetic", UID: "current", UIDKind: "vcard_uid",
		UIDState: "current", DisplayNameSnapshot: person.DisplayName}, person.Revision)
	require.NoError(t, err)
	person, _, err = source.PersonByID(ctx, person.PersonID)
	require.NoError(t, err)
	file, err := source.CreateFile(ctx, source.RootID(), "person-metadata.txt", metadataHashCurrent, 9, "text/plain",
		BlobPhysical{Encoding: "raw", StoredBytes: 9, PackEligible: true, Created: true})
	require.NoError(t, err)
	nodeID, versionID := file.ID, file.CurrentVersionID
	_, err = source.SetCustodian(ctx, CustodianRequest{Scope: CustodianScope{Kind: "document", NodeID: nodeID,
		ContentVersionID: versionID}, PersonID: person.PersonID, RawLabel: person.DisplayName,
		Rank: "primary", Basis: "operator_assigned", IfMatchRevision: 1})
	require.NoError(t, err)
	_, err = source.AssertDocumentPerson(ctx, PersonDocumentAssertion{ContentVersionID: versionID,
		PersonID: person.PersonID, Role: "author", Action: "assert", Note: "synthetic", Revision: 1})
	require.NoError(t, err)
	evidence, err := canonical.Marshal([]PersonCandidateOccurrence{{ContentVersionID: versionID, Role: "author",
		EvidenceKind: "source_metadata", EvidenceID: "claim-1"}})
	require.NoError(t, err)
	require.NoError(t, source.withLogicalTx(ctx, func(tx *sql.Tx) error {
		_, _, err := source.OpenPersonCandidate(ctx, tx, PersonMatchCandidate{ActorKey: "name_alias:ada lovelace",
			DisplayName: person.DisplayName, SuggestedPersonID: person.PersonID, Reason: "name_only", Evidence: evidence})
		return err
	}))
	absorbed, err := source.CreatePerson(ctx, "Grace Hopper", "operator")
	require.NoError(t, err)
	mergeID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = source.MergePersons(ctx, person.PersonID, absorbed.PersonID, mergeID, person.Revision, absorbed.Revision)
	require.NoError(t, err)
	person, _, err = source.PersonByID(ctx, person.PersonID)
	require.NoError(t, err)
	splitID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = source.SplitPerson(ctx, PersonSplitRequest{PersonID: person.PersonID, OperationID: splitID,
		DisplayName: "Ada Byron", Revision: person.Revision, IdentityIDs: []string{identity.IdentityID}})
	require.NoError(t, err)
	var exported bytes.Buffer
	require.NoError(t, source.ExportMetadata(ctx, &exported))
	for _, recordType := range []string{"person", "person_identity", "person_external_identity", "person_external_uid_alias",
		"person_alias", "person_merge", "person_split", "custodian_assignment", "person_document_assertion", "person_match_candidate"} {
		require.Contains(t, exported.String(), `"type":"`+recordType+`"`)
	}
	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(ctx, bytes.NewReader(exported.Bytes())))
	var restored bytes.Buffer
	require.NoError(t, target.ExportMetadata(ctx, &restored))
	require.Equal(t, exported.Bytes(), restored.Bytes())
}

func TestMergedThenRetiredPersonMetadataRoundTrip(t *testing.T) {
	source := newTestStore(t)
	ctx := t.Context()
	survivor, err := source.CreatePerson(ctx, "Synthetic Survivor", "operator")
	require.NoError(t, err)
	absorbed, err := source.CreatePerson(ctx, "Synthetic Absorbed", "operator")
	require.NoError(t, err)
	operationID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = source.MergePersons(ctx, survivor.PersonID, absorbed.PersonID, operationID, survivor.Revision, absorbed.Revision)
	require.NoError(t, err)
	survivor, _, err = source.PersonByID(ctx, survivor.PersonID)
	require.NoError(t, err)
	_, err = source.RetirePerson(ctx, survivor.PersonID, survivor.Revision)
	require.NoError(t, err)
	var exported bytes.Buffer
	require.NoError(t, source.ExportMetadata(ctx, &exported))
	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(ctx, bytes.NewReader(exported.Bytes())))
	var repeated bytes.Buffer
	require.NoError(t, target.ExportMetadata(ctx, &repeated))
	require.Equal(t, exported.Bytes(), repeated.Bytes())
}

func TestPersonAndPackageCustodiansMetadataRoundTrip(t *testing.T) {
	source := newTestStore(t)
	ctx := t.Context()
	pkg, node := seedReceivedPackage(t, source, "person-restore")
	commitReceivedLabel(t, source, pkg, node.CurrentVersionID, "PER000001")
	recordKey, err := PackageRecordKey("VOL001.dat", 1, "DOC-A")
	require.NoError(t, err)
	person, err := source.CreatePerson(ctx, "Synthetic Custodian", "operator")
	require.NoError(t, err)
	for _, scope := range []CustodianScope{
		{Kind: "package", PackageID: pkg.PackageID},
		{Kind: "package", PackageID: pkg.PackageID, PackageRecordID: recordKey, HasPackageRecordID: true},
	} {
		_, err := source.SetCustodian(ctx, CustodianRequest{Scope: scope, PersonID: person.PersonID,
			RawLabel: person.DisplayName, Rank: "primary", Basis: "package_column", SourceRef: "Custodian", IfMatchRevision: 1})
		require.NoError(t, err)
	}
	var exported bytes.Buffer
	require.NoError(t, source.ExportMetadata(ctx, &exported))
	require.Contains(t, exported.String(), `"type":"person"`)
	require.Contains(t, exported.String(), `"type":"custodian_assignment"`)
	backup, err := source.BeginMetadataSnapshot(ctx)
	require.NoError(t, err)
	var backupData bytes.Buffer
	require.NoError(t, backup.ExportBackup(ctx, &backupData))
	require.NoError(t, backup.Close())
	require.Contains(t, backupData.String(), `"type":"custodian_assignment"`)
	malformed := bytes.Replace(exported.Bytes(), []byte(`"package_record_id":""`), []byte(`"package_record_id":null`), 1)
	require.NotEqual(t, exported.Bytes(), malformed)
	corruptTarget := newTestStore(t)
	require.ErrorContains(t, corruptTarget.ImportMetadata(ctx, bytes.NewReader(malformed)), "invalid custodian assignment coordinates")

	target := newTestStore(t)
	require.NoError(t, target.ImportMetadata(ctx, bytes.NewReader(exported.Bytes())))
	got, _, err := target.PersonByID(ctx, person.PersonID)
	require.NoError(t, err)
	require.Equal(t, person, got)
	for _, scope := range []CustodianScope{
		{Kind: "package", PackageID: pkg.PackageID, HasPackageRecordID: true},
		{Kind: "package", PackageID: pkg.PackageID, PackageRecordID: recordKey, HasPackageRecordID: true},
	} {
		assignments, total, err := target.Custodians(ctx, scope, false, 10, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Equal(t, person.PersonID, *assignments[0].PersonID)
	}
	var repeated bytes.Buffer
	require.NoError(t, target.ExportMetadata(ctx, &repeated))
	require.Equal(t, exported.Bytes(), repeated.Bytes())
	_, err = target.db.ExecContext(ctx, `UPDATE custodian_assignments SET package_record_id='missing' WHERE package_record_id=?`, recordKey)
	require.NoError(t, err)
	require.ErrorContains(t, target.ExportMetadata(ctx, &bytes.Buffer{}), "custodian scope authority")
}

func TestCustodianPackageScopeRequiresExplicitExistingRecord(t *testing.T) {
	require.Error(t, validateCustodianScope(CustodianScope{Kind: "package", PackageID: "package-a", PackageRecordID: "record-a"}))
	s := newTestStore(t)
	request := CustodianRequest{Scope: CustodianScope{Kind: "package", PackageID: "missing"},
		RawLabel: "Synthetic", Rank: "primary", Basis: "package_column", IfMatchRevision: 1}
	_, err := s.SetCustodian(t.Context(), request)
	require.ErrorIs(t, err, ErrNotFound)
	pkg, _ := seedReceivedPackage(t, s, "scope-validation")
	request.Scope.PackageID = pkg.PackageID
	request.Scope.PackageRecordID = "missing"
	request.Scope.HasPackageRecordID = true
	_, err = s.SetCustodian(t.Context(), request)
	require.ErrorIs(t, err, ErrNotFound)
}
