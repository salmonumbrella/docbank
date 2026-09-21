package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seededPackageImportJob(t *testing.T, s *Store) PackageImportJobRequest {
	t.Helper()
	pkg, _ := seedReceivedPackage(t, s, "leased")
	preflightID, err := newUUIDv4()
	require.NoError(t, err)
	owner := "synthetic-operator"
	_, err = s.PutPackagePreflight(t.Context(), PackagePreflightRecord{
		PreflightID: preflightID, Owner: owner, SourceKind: "root", SourceRef: "synthetic-root-digest",
		SourceLocator: "synthetic-root", ProfileJSON: pkg.ProfileJSON, MappingJSON: pkg.MappingJSON,
		ProfileSHA256: pkg.ProfileSHA256, MappingSHA256: pkg.MappingSHA256,
		ManifestSHA256: pkg.ManifestBlobSHA256, ManifestBlobSHA256: pkg.ManifestBlobSHA256,
		CanonicalJSON: []byte(`{}`), DiagnosticsJSON: []byte(`[]`),
	})
	require.NoError(t, err)
	storedPreflight, err := s.PackagePreflight(t.Context(), owner, preflightID)
	require.NoError(t, err)
	require.Equal(t, "synthetic-root", storedPreflight.SourceLocator)
	require.Equal(t, pkg.ProfileJSON, storedPreflight.ProfileJSON)
	require.Equal(t, pkg.MappingJSON, storedPreflight.MappingJSON)
	operationID, err := newUUIDv4()
	require.NoError(t, err)
	jobID, err := newUUIDv4()
	require.NoError(t, err)
	return PackageImportJobRequest{ID: jobID, Owner: owner, OperationID: operationID,
		RequestSHA256: fakeHash("ab"), PreflightID: preflightID, PackageID: pkg.PackageID,
		JobJSON: []byte(`{"accept_partial":false,"index_supplied_text":true}`)}
}

func TestPackageImportJobAdmissionReplaysExactOperation(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	first, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, packageJobQueued, first.State)
	replay, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	retry := request
	retry.ID, err = newUUIDv4()
	require.NoError(t, err)
	replay, err = s.CreatePackageImportJob(t.Context(), retry)
	require.NoError(t, err, "retry identity is the operation and request, not a new internal job id")
	require.Equal(t, first, replay)
	changed := request
	changed.RequestSHA256 = fakeHash("ac")
	_, err = s.CreatePackageImportJob(t.Context(), changed)
	require.ErrorIs(t, err, ErrPackageConflict)
}

func TestPackageImportJobAdmissionRejectsSecondOperationForPackage(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	first, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	second := request
	second.ID, err = newUUIDv4()
	require.NoError(t, err)
	second.OperationID, err = newUUIDv4()
	require.NoError(t, err)
	_, err = s.CreatePackageImportJob(t.Context(), second)
	require.ErrorIs(t, err, ErrPackageConflict)
	got, err := s.PackageImportJob(t.Context(), request.Owner, request.OperationID)
	require.NoError(t, err)
	require.Equal(t, first, got)
}

func TestAdmitPackageImportPublishesIngestPackageAndJobAtomically(t *testing.T) {
	s := newTestStore(t)
	request := validPackageRequest(t, s)
	run, err := s.BeginIngest(t.Context(), "package:loadfile", "synthetic-package")
	require.NoError(t, err)
	request.SnapshotID = ""
	request.Direction = packageDirectionReceived
	request.State = "importing"
	request.IngestID = run.ID()
	request.ManifestSHA256 = request.ManifestBlobSHA256
	owner := "synthetic-operator"
	preflightID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.PutPackagePreflight(t.Context(), PackagePreflightRecord{
		PreflightID: preflightID, Owner: owner, SourceKind: "root", SourceRef: "synthetic-root-digest",
		SourceLocator: "synthetic-root", ProfileJSON: request.ProfileJSON, MappingJSON: request.MappingJSON,
		ProfileSHA256: request.ProfileSHA256, MappingSHA256: request.MappingSHA256,
		ManifestSHA256: request.ManifestSHA256, ManifestBlobSHA256: request.ManifestBlobSHA256,
		CanonicalJSON: []byte(`{}`), DiagnosticsJSON: []byte(`[]`),
	})
	require.NoError(t, err)
	operationID, err := newUUIDv4()
	require.NoError(t, err)
	jobID, err := newUUIDv4()
	require.NoError(t, err)
	jobRequest := PackageImportJobRequest{ID: jobID, Owner: owner, OperationID: operationID,
		RequestSHA256: fakeHash("a1"), PreflightID: preflightID, PackageID: request.PackageID,
		JobJSON: []byte(`{"source_kind":"root","source_locator":"synthetic-root"}`)}
	bad := jobRequest
	bad.PreflightID, err = newUUIDv4()
	require.NoError(t, err)
	_, err = s.AdmitPackageImport(t.Context(), run, request, bad)
	require.ErrorIs(t, err, ErrPackageConflict)
	var retained int
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ingests WHERE id=?`, run.ID()).Scan(&retained))
	require.Zero(t, retained, "failed admission must not retain a source collection")
	_, err = s.Package(t.Context(), request.PackageID)
	require.ErrorIs(t, err, ErrNotFound)

	first, err := s.AdmitPackageImport(t.Context(), run, request, jobRequest)
	require.NoError(t, err)
	require.Equal(t, packageJobQueued, first.State)
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ingests WHERE id=? AND source_kind='package:loadfile'`, run.ID()).Scan(&retained))
	require.Equal(t, 1, retained)
	pkg, err := s.Package(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Equal(t, request.IngestID, pkg.IngestID)
	loadedRun, err := s.PackageIngestRun(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Equal(t, run.record, loadedRun.record)
	competing := jobRequest
	competing.ID, err = newUUIDv4()
	require.NoError(t, err)
	competing.OperationID, err = newUUIDv4()
	require.NoError(t, err)
	_, err = s.AdmitPackageImport(t.Context(), run, request, competing)
	require.ErrorIs(t, err, ErrPackageConflict)

	retryRun, err := s.BeginIngest(t.Context(), "package:loadfile", "synthetic-package")
	require.NoError(t, err)
	retryPackage := request
	retryPackage.PackageID, err = newUUIDv4()
	require.NoError(t, err)
	retryPackage.IngestID = retryRun.ID()
	retryJob := jobRequest
	retryJob.ID, err = newUUIDv4()
	require.NoError(t, err)
	retryJob.PackageID = retryPackage.PackageID
	replayed, err := s.AdmitPackageImport(t.Context(), retryRun, retryPackage, retryJob)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ingests WHERE id=?`, retryRun.ID()).Scan(&retained))
	require.Zero(t, retained, "replay must not create a second source collection")

	changed := retryJob
	changed.RequestSHA256 = fakeHash("a2")
	_, err = s.AdmitPackageImport(t.Context(), retryRun, retryPackage, changed)
	require.ErrorIs(t, err, ErrPackageConflict)
	wrongSource := jobRequest
	wrongSource.OperationID, err = newUUIDv4()
	require.NoError(t, err)
	wrongSource.ID, err = newUUIDv4()
	require.NoError(t, err)
	wrongSource.PackageID = retryPackage.PackageID
	wrongSource.JobJSON = []byte(`{"source_kind":"root","source_locator":"different-root"}`)
	_, err = s.AdmitPackageImport(t.Context(), retryRun, retryPackage, wrongSource)
	require.ErrorIs(t, err, ErrPackageConflict)
}

func TestPackageImportJobCancelFencesStaleFinalization(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	claimed, err := s.ClaimPackageImportJob(t.Context(), "worker-one", 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, packageJobRunning, claimed.State)
	require.EqualValues(t, 1, claimed.Epoch)
	require.NotEmpty(t, claimed.Token)
	cancelled, err := s.CancelPackageImportJob(t.Context(), request.Owner, request.OperationID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	_, err = s.FinishPackageImportJob(t.Context(), claimed.ID, claimed.Epoch, claimed.Token,
		"complete", "")
	require.ErrorIs(t, err, ErrPackageConflict)
	pkg, err := s.Package(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", pkg.State)
}

func TestPackageImportLeaseReclaimInvalidatesEarlierToken(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	first, err := s.ClaimPackageImportJob(t.Context(), "worker-one", time.Minute)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE package_import_jobs SET lease_expires_at=? WHERE id=?`,
		"2000-01-01T00:00:00Z", first.ID)
	require.NoError(t, err)
	second, err := s.ClaimPackageImportJob(t.Context(), "worker-two", time.Minute)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Greater(t, second.Epoch, first.Epoch)
	require.NotEqual(t, first.Token, second.Token)
	_, err = s.FinishPackageImportJob(t.Context(), first.ID, first.Epoch, first.Token,
		"failed", "")
	require.ErrorIs(t, err, ErrPackageConflict)
	finished, err := s.FinishPackageImportJob(t.Context(), second.ID, second.Epoch, second.Token,
		"failed", "")
	require.NoError(t, err)
	require.Equal(t, "failed", finished.State)
}

func TestPackageImportGapReceiptIsFencedAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	job, err := s.ClaimPackageImportJob(t.Context(), "gap-worker", time.Minute)
	require.NoError(t, err)
	key, err := PackageRecordKey("VOL001.dat", 1, "DOC-MISSING")
	require.NoError(t, err)
	raw := []byte(`{"doc_id":"DOC-MISSING"}`)
	digest := sha256.Sum256(raw)
	record := PackageRecordRow{PackageID: request.PackageID, RowID: key, LoadFile: "VOL001.dat",
		RowOrdinal: 1, OccurrenceID: PackageOccurrenceID(request.PackageID, key),
		RawJSON: raw, RawSHA256: hex.EncodeToString(digest[:])}
	receiptID, err := newUUIDv4()
	require.NoError(t, err)
	receipt := PackageImportReceipt{ReceiptID: receiptID, PackageID: request.PackageID,
		RecordKey: key, OccurrenceID: record.OccurrenceID, State: "rejected",
		ReceiptJSON: []byte(`{"reason":"missing_declared_file"}`)}
	_, err = s.RecordPackageGapWithLease(t.Context(), job.ID, job.Epoch, "stale", record, receipt)
	require.ErrorIs(t, err, ErrPackageConflict)
	_, err = s.PackageImportHead(t.Context(), request.PackageID, key)
	require.ErrorIs(t, err, ErrNotFound)
	first, err := s.RecordPackageGapWithLease(t.Context(), job.ID, job.Epoch, job.Token, record, receipt)
	require.NoError(t, err)
	require.Empty(t, first.ContentVersionID)
	replayed, err := s.RecordPackageGapWithLease(t.Context(), job.ID, job.Epoch, job.Token, record, receipt)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	changed := receipt
	changed.State = "skipped"
	_, err = s.RecordPackageGapWithLease(t.Context(), job.ID, job.Epoch, job.Token, record, changed)
	require.ErrorIs(t, err, ErrPackageConflict)
}

func TestPackageImportProgressIncludesGapsOnCommittedRecords(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	job, err := s.ClaimPackageImportJob(t.Context(), "gap-worker", time.Minute)
	require.NoError(t, err)
	var versionID string
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT b.content_version_id
		FROM provenance_version_bindings b JOIN provenance p ON p.identity=b.provenance_identity
		JOIN packages k ON k.ingest_id=p.ingest_id WHERE k.package_id=? LIMIT 1`, request.PackageID).Scan(&versionID))
	key, err := PackageRecordKey("VOL001.dat", 1, "DOC-A")
	require.NoError(t, err)
	occurrence := PackageOccurrenceID(request.PackageID, key)
	raw := []byte(`{"doc_id":"DOC-A"}`)
	digest := sha256.Sum256(raw)
	receiptID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.CommitPackageRecordWithLease(t.Context(), job.ID, job.Epoch, job.Token,
		PackageRecordRow{PackageID: request.PackageID, RowID: key, LoadFile: "VOL001.dat",
			RowOrdinal: 1, OccurrenceID: occurrence, RawJSON: raw,
			RawSHA256: hex.EncodeToString(digest[:])}, nil,
		PackageImportReceipt{ReceiptID: receiptID, PackageID: request.PackageID, RecordKey: key,
			OccurrenceID: occurrence, ContentVersionID: versionID, State: "committed",
			ReceiptJSON: []byte(`{"gaps":["VOL001/TEXT/A.txt"]}`)})
	require.NoError(t, err)

	progress, err := s.PackageImportProgress(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Equal(t, 1, progress.Committed)
	require.Equal(t, 1, progress.GapCount)
	require.Equal(t, []string{"VOL001/TEXT/A.txt"}, progress.Gaps)
}

func TestPackageImportProgressNamesRejectedRecordWithEmptyGapArray(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	job, err := s.ClaimPackageImportJob(t.Context(), "gap-worker", time.Minute)
	require.NoError(t, err)
	key, err := PackageRecordKey("VOL001.dat", 1, "DOC-EMPTY")
	require.NoError(t, err)
	occurrence := PackageOccurrenceID(request.PackageID, key)
	raw := []byte(`{"doc_id":"DOC-EMPTY"}`)
	digest := sha256.Sum256(raw)
	receiptID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.RecordPackageGapWithLease(t.Context(), job.ID, job.Epoch, job.Token,
		PackageRecordRow{PackageID: request.PackageID, RowID: key, LoadFile: "VOL001.dat",
			RowOrdinal: 1, OccurrenceID: occurrence, RawJSON: raw,
			RawSHA256: hex.EncodeToString(digest[:])},
		PackageImportReceipt{ReceiptID: receiptID, PackageID: request.PackageID,
			RecordKey: key, OccurrenceID: occurrence, State: "rejected",
			ReceiptJSON: []byte(`{"gaps":[]}`)})
	require.NoError(t, err)

	progress, err := s.PackageImportProgress(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Zero(t, progress.Committed)
	require.Equal(t, 1, progress.GapCount)
	require.Equal(t, []string{key}, progress.Gaps)
}

func TestPackageImportLeaseMayBeReleasedForImmediateRetry(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	claimed, err := s.ClaimPackageImportJob(t.Context(), "first", time.Minute)
	require.NoError(t, err)
	_, err = s.ReleasePackageImportJob(t.Context(), claimed.ID, claimed.Epoch, "stale")
	require.ErrorIs(t, err, ErrPackageConflict)
	released, err := s.ReleasePackageImportJob(t.Context(), claimed.ID, claimed.Epoch, claimed.Token)
	require.NoError(t, err)
	require.Equal(t, packageJobQueued, released.State)
	require.Empty(t, released.Token)
	reclaimed, err := s.ClaimPackageImportJob(t.Context(), "second", time.Minute)
	require.NoError(t, err)
	require.Equal(t, claimed.ID, reclaimed.ID)
	require.Greater(t, reclaimed.Epoch, claimed.Epoch)
}

func TestStalePackageWorkerCannotCommitARecordAfterCancel(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	claimed, err := s.ClaimPackageImportJob(t.Context(), "worker-one", 5*time.Minute)
	require.NoError(t, err)
	_, err = s.CancelPackageImportJob(t.Context(), request.Owner, request.OperationID)
	require.NoError(t, err)
	key, err := PackageRecordKey("VOL001.dat", 1, "DOC-A")
	require.NoError(t, err)
	var versionID string
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT b.content_version_id
		FROM provenance_version_bindings b JOIN provenance p ON p.identity=b.provenance_identity
		JOIN packages k ON k.ingest_id=p.ingest_id WHERE k.package_id=? LIMIT 1`, request.PackageID).Scan(&versionID))
	occurrence := PackageOccurrenceID(request.PackageID, key)
	raw := []byte(`{}`)
	digest := sha256.Sum256(raw)
	receiptID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.CommitPackageRecordWithLease(t.Context(), claimed.ID, claimed.Epoch, claimed.Token,
		PackageRecordRow{PackageID: request.PackageID, RowID: key, LoadFile: "VOL001.dat",
			RowOrdinal: 1, OccurrenceID: occurrence, RawJSON: raw,
			RawSHA256: hex.EncodeToString(digest[:])}, nil,
		PackageImportReceipt{ReceiptID: receiptID, PackageID: request.PackageID, RecordKey: key,
			OccurrenceID: occurrence, ContentVersionID: versionID, State: "committed", ReceiptJSON: []byte(`{}`)})
	require.ErrorIs(t, err, ErrPackageConflict)
	_, err = s.PackageRecord(t.Context(), request.PackageID, key)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPackageImportFinishRejectsUnrelatedSnapshot(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	claimed, err := s.ClaimPackageImportJob(t.Context(), "worker-one", 5*time.Minute)
	require.NoError(t, err)
	var unrelated string
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`SELECT snapshot_id FROM collection_snapshots ORDER BY snapshot_id LIMIT 1`).Scan(&unrelated))
	_, err = s.FinishPackageImportJob(t.Context(), claimed.ID, claimed.Epoch, claimed.Token,
		"complete", unrelated)
	require.ErrorIs(t, err, ErrPackageConflict)
	pkg, err := s.Package(t.Context(), request.PackageID)
	require.NoError(t, err)
	require.Equal(t, "importing", pkg.State)
}

func TestPackageImportFinishPublishesOnlyMatchingReceiptSnapshot(t *testing.T) {
	s := newTestStore(t)
	request := seededPackageImportJob(t, s)
	_, err := s.CreatePackageImportJob(t.Context(), request)
	require.NoError(t, err)
	claimed, err := s.ClaimPackageImportJob(t.Context(), "worker-one", 5*time.Minute)
	require.NoError(t, err)
	pkg, err := s.Package(t.Context(), request.PackageID)
	require.NoError(t, err)
	var nodeID int64
	var versionID, blobSHA, name string
	var size int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT n.id,v.version_id,v.blob_hash,v.size,n.name
		FROM provenance p JOIN nodes n ON n.id=p.node_id
		JOIN content_versions v ON v.version_id=n.current_version_id
		WHERE p.ingest_id=? LIMIT 1`, pkg.IngestID).Scan(&nodeID, &versionID, &blobSHA, &size, &name))
	key, err := PackageRecordKey("VOL001.dat", 1, "DOC-A")
	require.NoError(t, err)
	occurrence := PackageOccurrenceID(pkg.PackageID, key)
	raw := []byte(`{}`)
	hash := sha256.Sum256(raw)
	receiptID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.CommitPackageRecordWithLease(t.Context(), claimed.ID, claimed.Epoch, claimed.Token,
		PackageRecordRow{PackageID: pkg.PackageID, RowID: key, LoadFile: "VOL001.dat", RowOrdinal: 1,
			OccurrenceID: occurrence, RawJSON: raw, RawSHA256: hex.EncodeToString(hash[:])}, nil,
		PackageImportReceipt{ReceiptID: receiptID, PackageID: pkg.PackageID, RecordKey: key,
			OccurrenceID: occurrence, ContentVersionID: versionID, State: "committed", ReceiptJSON: []byte(`{}`)})
	require.NoError(t, err)
	snapshotID, err := newUUIDv4()
	require.NoError(t, err)
	_, err = s.SealCollectionSnapshot(t.Context(), SnapshotSealRequest{SnapshotID: snapshotID,
		SourceCollectionIDs: []string{pkg.IngestID}, Members: []CollectionSnapshotMember{{
			Ordinal: 1, OccurrenceID: occurrence, NodeID: nodeID, ContentVersionID: versionID,
			BlobSHA256: blobSHA, Size: size, FamilyID: occurrence, FamilyOrder: 1,
			DisplayName: name, FrozenFieldsJSON: "{}", DocumentKind: "other",
		}}})
	require.NoError(t, err)
	finished, err := s.FinishPackageImportJob(t.Context(), claimed.ID, claimed.Epoch, claimed.Token,
		"complete", snapshotID)
	require.NoError(t, err)
	require.Equal(t, "complete", finished.State)
	pkg, err = s.Package(t.Context(), pkg.PackageID)
	require.NoError(t, err)
	require.Equal(t, snapshotID, pkg.SnapshotID)
	require.Equal(t, "complete", pkg.State)
}
