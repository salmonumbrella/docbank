package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/internal/store"
	docsqlite "go.kenn.io/docbank/sqlite"
	"go.kenn.io/kit/packstore"
)

type okfCountingReader struct {
	calls  int
	stream packstore.VerifiedReadCloser
	size   int64
	err    error
}

func (r *okfCountingReader) OpenStreamContext(context.Context, string) (packstore.VerifiedReadCloser, int64, error) {
	r.calls++
	return r.stream, r.size, r.err
}

type okfTestStream struct {
	*instrumentedVerifiedStream
	expected  string
	readBytes []byte
}

func (s *okfTestStream) Verified() bool {
	if !s.instrumentedVerifiedStream.Verified() {
		return false
	}
	sum := sha256.Sum256(s.readBytes)
	return hex.EncodeToString(sum[:]) == s.expected
}

func (s *okfTestStream) Read(p []byte) (int, error) {
	n, err := s.instrumentedVerifiedStream.Read(p)
	s.readBytes = append(s.readBytes, p[:n]...)
	if errors.Is(err, io.EOF) {
		if !s.Verified() {
			return n, errors.New("checksum mismatch")
		}
		return n, io.EOF
	}
	return n, err
}

type okfCancelStream struct {
	*okfTestStream

	cancel context.CancelFunc
}

func (s *okfCancelStream) Read(p []byte) (int, error) {
	n, err := s.okfTestStream.Read(p)
	s.cancel()
	return n, err
}

func TestOKFOriginalStreamBoundsAndIntegrity(t *testing.T) {
	t.Parallel()
	data := "synthetic original"
	sum := sha256.Sum256([]byte(data))
	hash := hex.EncodeToString(sum[:])
	boundary := &okfCountingReader{err: errors.New("opened")}
	require.ErrorIs(t, verifyOKFOriginal(t.Context(), boundary, hash, bundle.MaxRoleBytes), bundle.ErrUnavailable)
	require.Equal(t, 1, boundary.calls)
	boundary.calls = 0
	require.ErrorIs(t, verifyOKFOriginal(t.Context(), boundary, hash, bundle.MaxRoleBytes+1), bundle.ErrLimit)
	require.Zero(t, boundary.calls)
	for _, tc := range []struct {
		name, text, hash string
		size             int64
		wantError        bool
	}{
		{"valid", data, hash, int64(len(data)), false},
		{"short", data[:len(data)-1], hash, int64(len(data)), true},
		{"tampered", "synthetix original", hash, int64(len(data)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &instrumentedVerifiedStream{reader: strings.NewReader(tc.text)}
			reader := &okfCountingReader{stream: &okfTestStream{instrumentedVerifiedStream: stream, expected: tc.hash}, size: tc.size}
			err := verifyOKFOriginal(t.Context(), reader, tc.hash, tc.size)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.True(t, stream.closed)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &okfCountingReader{stream: &instrumentedVerifiedStream{reader: strings.NewReader(data)}, size: int64(len(data))}
	require.ErrorIs(t, verifyOKFOriginal(ctx, reader, hash, int64(len(data))), context.Canceled)
	require.Zero(t, reader.calls)
	expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stop()
	require.ErrorIs(t, verifyOKFOriginal(expired, reader, hash, int64(len(data))), context.DeadlineExceeded)
	require.Zero(t, reader.calls)
	mid, stopMid := context.WithCancel(t.Context())
	defer stopMid()
	underlying := &instrumentedVerifiedStream{reader: strings.NewReader(data)}
	reader = &okfCountingReader{stream: &okfCancelStream{okfTestStream: &okfTestStream{instrumentedVerifiedStream: underlying, expected: hash}, cancel: stopMid}, size: int64(len(data))}
	require.ErrorIs(t, verifyOKFOriginal(mid, reader, hash, int64(len(data))), context.Canceled)
	require.True(t, underlying.closed)
}

func TestOKFWorkerReceiptAndReattachment(t *testing.T) {
	t.Parallel()
	f := newPublicationFixture(t)
	provider := newWorkerProvider(t)
	profile := workerProcessingProfile(t, provider.Descriptor())
	request := workerJobRequest(f.versionID, profile, provider.Descriptor())
	grantWorkerConsent(t, f.catalog, request)
	_, _, err := f.catalog.EnqueueRenditionJob(t.Context(), request)
	require.NoError(t, err)
	second, err := f.catalog.CreateFile(t.Context(), f.catalog.RootID(), "reattached.pdf", f.mustSourceHash(), int64(len(workerSourceBytes)), "application/pdf")
	require.NoError(t, err)
	request.ContentVersionID = second.CurrentVersionID
	_, _, err = f.catalog.EnqueueRenditionJob(t.Context(), request)
	require.NoError(t, err)
	now := time.Now().UTC()
	gate := newTestOperationGate()
	worker, err := NewRenditionWorker(RenditionWorkerConfig{Catalog: f.catalog, Blobs: f.blobs, Runtime: workerRuntime{provider: provider}, Gate: gate, Owner: "okf-worker-test", LeaseDuration: time.Minute, IdleDelay: time.Millisecond, Clock: func() time.Time { return now }})
	require.NoError(t, err)
	processed, err := worker.RunOne(t.Context())
	require.NoError(t, err)
	require.True(t, processed)
	for _, version := range []string{f.versionID, second.CurrentVersionID} {
		view, e := f.catalog.ActiveRendition(t.Context(), version, profile.Fingerprint)
		require.NoError(t, e)
		v, e := f.catalog.ContentVersionByID(t.Context(), version)
		require.NoError(t, e)
		node, e := f.catalog.NodeByID(t.Context(), v.NodeID)
		require.NoError(t, e)
		member := bundle.Member{NodeID: node.ID, VersionID: version, SHA256: v.BlobHash, Size: v.Size, Revision: node.Revision}
		source, e := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
		require.NoError(t, e)
		plan, e := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: view.Attachment.ID, BuildID: view.Build.ID}}, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
		require.NoError(t, e)
		require.NoError(t, f.catalog.WalkExportDocuments(t.Context(), plan.ID, func(d bundle.Document) error {
			var receipt bundle.OKFReceipt
			require.NoError(t, json.Unmarshal(d.Roles[0].Recipe, &receipt))
			require.NotNil(t, receipt.Generated)
			require.Equal(t, "process:synthetic.worker-v1", receipt.Generated.By)
			completed, e := time.Parse(time.RFC3339Nano, view.Build.CompletedAt)
			require.NoError(t, e)
			generated, e := time.Parse(time.RFC3339Nano, receipt.Generated.At)
			require.NoError(t, e)
			require.True(t, completed.Equal(generated))
			return nil
		}))
	}
	// Exercise malformed retained evidence against a real worker-produced snapshot.
	view, err := f.catalog.ActiveRendition(t.Context(), f.versionID, profile.Fingerprint)
	require.NoError(t, err)
	v, err := f.catalog.ContentVersionByID(t.Context(), f.versionID)
	require.NoError(t, err)
	node, err := f.catalog.NodeByID(t.Context(), v.NodeID)
	require.NoError(t, err)
	member := bundle.Member{NodeID: node.ID, VersionID: f.versionID, SHA256: v.BlobHash, Size: v.Size, Revision: node.Revision}
	src, err := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	req := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: src.ID, MemberHash: src.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: view.Attachment.ID, BuildID: view.Build.ID}}
	db, err := store.DefaultSQLiteDriver().Open(f.databasePath, docsqlite.OpenOptions{Access: docsqlite.ReadWriteExisting, TransactionMode: docsqlite.Immediate})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `DROP TRIGGER rendition_builds_immutable_update`)
	require.NoError(t, err)
	var originalReceipt, originalChecksum, originalSnapshot string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT provider_receipt_json,authorization_checksum FROM rendition_builds WHERE build_id=?`, view.Build.ID).Scan(&originalReceipt, &originalChecksum))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT execution_snapshot_json FROM rendition_jobs WHERE job_id=?`, view.Build.ID).Scan(&originalSnapshot))
	for _, tc := range []struct {
		name, field, value                     string
		missing, checksum, conflict, generated bool
	}{
		{name: "unknown member", field: "future_member", value: "retained", generated: true},
		{name: "descriptor", field: "descriptor_fingerprint", value: strings.Repeat("f", 64)},
		{name: "policy", field: "policy_fingerprint", value: strings.Repeat("f", 64)},
		{name: "authorization fingerprint", field: "authorization_fingerprint", value: strings.Repeat("f", 64)},
		{name: "started timestamp", field: "started_at", value: "2020-01-01T00:00:00Z"},
		{name: "completed timestamp", field: "completed_at", value: "2020-01-01T00:00:00Z"},
		{name: "missing snapshot", missing: true}, {name: "authorization checksum", checksum: true},
		{name: "wrong source with unknown member", field: "source_sha256", value: strings.Repeat("f", 64), conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(originalReceipt), &fields))
			if tc.field != "" {
				fields[tc.field] = tc.value
			}
			if tc.conflict {
				fields["future_member"] = true
			}
			raw, e := json.Marshal(fields)
			require.NoError(t, e)
			checksum := originalChecksum
			if tc.checksum {
				checksum = strings.Repeat("f", 64)
			}
			_, e = db.ExecContext(t.Context(), `UPDATE rendition_builds SET provider_receipt_json=?, authorization_checksum=? WHERE build_id=?`, string(raw), checksum, view.Build.ID)
			require.NoError(t, e)
			var snapshot any = originalSnapshot
			if tc.missing {
				snapshot = nil
			}
			_, e = db.ExecContext(t.Context(), `UPDATE rendition_jobs SET execution_snapshot_json=? WHERE job_id=?`, snapshot, view.Build.ID)
			require.NoError(t, e)
			input, e := f.catalog.ExportOKFInput(t.Context(), "owner", req)
			if tc.conflict {
				require.ErrorIs(t, e, bundle.ErrConflict)
				return
			}
			require.NoError(t, e)
			if tc.generated {
				require.NotNil(t, input.Generated)
			} else {
				require.Nil(t, input.Generated)
			}
		})
	}
}

func TestOKFReceiptVariants(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, provider, source string
		receiptJSON            string
		metadataMismatch       bool
		tampered               bool
		generated              bool
		conflict               bool
	}{
		{name: "unsubstantiated", provider: "invented-producer"},
		{name: "unsubstantiated docling", provider: "docling"},
		{name: "absent", receiptJSON: `{}`},
		{name: "malformed", receiptJSON: `{"provider_id":42}`},
		{name: "unknown field", receiptJSON: `{"unexpected":true}`},
		{name: "metadata mismatch", receiptJSON: `{}`, metadataMismatch: true, conflict: true},
		{name: "tampered artifact", receiptJSON: `{}`, tampered: true},
		{name: "wrong source", provider: "docling", source: strings.Repeat("f", 64), conflict: true},
		{name: "malformed fields", provider: "invalid provider", generated: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicationFixture(t)
			staged := f.stage(t, publicationIDs{"okf-build", "okf-attachment", "okf-generation"}, "synthetic text", "Synthetic heading")
			envelopeSource := staged.Build.SourceSHA256
			if tc.metadataMismatch {
				envelopeSource = strings.Repeat("f", 64)
			}
			enveloped, _, err := document.EnvelopeRenditionV1(staged.Rendition, document.RenditionEnvelopeV1{BuildID: staged.Build.ID, SourceSHA256: envelopeSource, SourceFormat: "pdf", SourceMediaType: "application/pdf", RenditionRequestFingerprint: staged.Build.RenditionRequestFingerprint, EvidenceLexicalFingerprint: staged.Build.EvidenceLexicalFingerprint, NormalizedEvidenceContract: document.NormalizedEvidenceContractV1, UnitKind: document.EvidenceUnitPage, Title: "Retained title"})
			require.NoError(t, err)
			staged.Build.MarkdownChecksum = enveloped.MarkdownChecksum
			staged.Build.RenditionChecksum = enveloped.Checksum
			staged.Build.Artifacts[1].BlobHash = enveloped.MarkdownChecksum
			staged.Build.Artifacts[1].Checksum = enveloped.MarkdownChecksum
			staged.Build.Artifacts[1].Size = int64(len(enveloped.Markdown))
			staged.Artifacts[1].Payload = bytes.NewReader(enveloped.Markdown)
			if tc.receiptJSON != "" {
				staged.Build.ProviderReceipt = jsontext.Value(tc.receiptJSON)
			} else {
				source := staged.Build.SourceSHA256
				if tc.source != "" {
					source = tc.source
				}
				raw, e := json.Marshal(document.RenditionReceipt{ProviderID: tc.provider, SourceSHA256: source, RenditionRequestFingerprint: staged.Build.RenditionRequestFingerprint, OperationID: staged.Build.ProviderOperationID, CompletedAt: staged.Build.CompletedAt})
				require.NoError(t, e)
				staged.Build.ProviderReceipt = raw
			}
			for i, artifact := range staged.Build.Artifacts {
				written, e := f.blobs.WriteDetailedContext(t.Context(), staged.Artifacts[i].Payload)
				require.NoError(t, e)
				require.Equal(t, artifact.BlobHash, written.Hash)
				require.NoError(t, f.catalog.RecordRenditionBlob(t.Context(), written.Hash, written.Size, processingBlobPhysical(t, written)))
			}
			require.NoError(t, f.catalog.StageRenditionBuild(t.Context(), staged.Build))
			generation, err := f.catalog.StageLexicalGeneration(t.Context(), staged.LexicalGenerationID)
			require.NoError(t, err)
			require.NoError(t, f.catalog.PublishRenditionAndLexicalHeads(t.Context(), staged.Attachment, staged.Head, generation.ID))
			node, err := f.catalog.NodeByPath(t.Context(), "/source.pdf")
			require.NoError(t, err)
			member := bundle.Member{NodeID: node.ID, VersionID: f.versionID, SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
			source, err := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
			require.NoError(t, err)
			req := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: f.profile.Fingerprint, AttachmentID: staged.Attachment.ID, BuildID: staged.Build.ID}}
			gate := newTestOperationGate()
			if tc.tampered {
				layout, e := packstore.NewLayout(filepath.Join(filepath.Dir(f.databasePath), "blobs"), packstore.LayoutOptions{Staging: packstore.StagingStoreDirectory, StagingDir: "tmp"})
				require.NoError(t, e)
				hash, e := packstore.ParseHash(staged.Build.MarkdownChecksum)
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(layout.LoosePath(hash), bytes.Repeat([]byte("x"), int(staged.Build.Artifacts[1].Size)), 0600))
			}
			plan, err := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
			if tc.tampered {
				require.ErrorIs(t, err, bundle.ErrUnavailable)
				return
			}
			if tc.conflict {
				require.ErrorIs(t, err, bundle.ErrConflict)
				return
			}
			require.NoError(t, err)
			require.NoError(t, f.catalog.WalkExportDocuments(t.Context(), plan.ID, func(d bundle.Document) error {
				var receipt bundle.OKFReceipt
				require.NoError(t, json.Unmarshal(d.Roles[0].Recipe, &receipt))
				if tc.generated {
					require.NotNil(t, receipt.Generated)
					require.Equal(t, "process:docling", receipt.Generated.By)
				} else {
					require.Nil(t, receipt.Generated)
				}
				stream, _, e := f.blobs.OpenStreamContext(t.Context(), d.Roles[0].SHA256)
				require.NoError(t, e)
				content, e := io.ReadAll(stream)
				require.NoError(t, e)
				require.NoError(t, stream.Close())
				require.Contains(t, string(content), `title: "Retained title"`)
				return nil
			}))
		})
	}
}

func TestOKFRejectsEmptyLegacy(t *testing.T) {
	t.Parallel()
	for _, mediaType := range []string{"application/pdf", "text/plain"} {
		t.Run(mediaType, func(t *testing.T) { testOKFRejectsEmptyLegacy(t, mediaType) })
	}
}

func testOKFRejectsEmptyLegacy(t *testing.T, mediaType string) {
	t.Helper()
	f := newPublicationFixture(t)
	original, err := f.catalog.NodeByPath(t.Context(), "/source.pdf")
	require.NoError(t, err)
	node, err := f.catalog.CreateFile(t.Context(), f.catalog.RootID(), "synthetic.txt", original.BlobHash, original.Size, mediaType)
	require.NoError(t, err)
	staged := f.stageForSource(t, publicationIDs{"nonpdf-build", "nonpdf-attachment", "nonpdf-generation"}, "synthetic text", "Synthetic heading", node.CurrentVersionID, node.BlobHash)
	empty, err := f.blobs.WriteDetailedContext(t.Context(), bytes.NewReader(nil))
	require.NoError(t, err)
	staged.Build.Artifacts[1].BlobHash, staged.Build.Artifacts[1].Checksum, staged.Build.Artifacts[1].Size = empty.Hash, empty.Hash, 0
	staged.Build.MarkdownChecksum = empty.Hash
	staged.Build.LexicalSegments = nil
	for i, artifact := range staged.Build.Artifacts {
		if i == 1 {
			require.NoError(t, f.catalog.RecordRenditionBlob(t.Context(), empty.Hash, empty.Size, processingBlobPhysical(t, empty)))
			continue
		}
		written, e := f.blobs.WriteDetailedContext(t.Context(), staged.Artifacts[i].Payload)
		require.NoError(t, e)
		require.Equal(t, artifact.BlobHash, written.Hash)
		require.NoError(t, f.catalog.RecordRenditionBlob(t.Context(), written.Hash, written.Size, processingBlobPhysical(t, written)))
	}
	require.NoError(t, f.catalog.StageRenditionBuild(t.Context(), staged.Build))
	generation, err := f.catalog.StageLexicalGeneration(t.Context(), staged.LexicalGenerationID)
	require.NoError(t, err)
	require.NoError(t, f.catalog.PublishRenditionAndLexicalHeads(t.Context(), staged.Attachment, staged.Head, generation.ID))
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	source, err := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	req := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: f.profile.Fingerprint, AttachmentID: staged.Attachment.ID, BuildID: staged.Build.ID}}
	gate := newTestOperationGate()
	_, err = PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "https://archive.example.invalid", gate.MutateContext, gate.MaintainContext)
	require.ErrorIs(t, err, bundle.ErrUnavailable)
}

func TestOKFExpiredSourcePrunedDuringPreparation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newPublicationFixture(t)
		staged := f.stage(t, publicationIDs{"expiry-build", "expiry-attachment", "expiry-generation"}, "synthetic text", "Synthetic heading")
		rendition, _, err := document.EnvelopeRenditionV1(staged.Rendition, document.RenditionEnvelopeV1{BuildID: staged.Build.ID, SourceSHA256: staged.Build.SourceSHA256, SourceFormat: "pdf", SourceMediaType: "application/pdf", RenditionRequestFingerprint: staged.Build.RenditionRequestFingerprint, EvidenceLexicalFingerprint: staged.Build.EvidenceLexicalFingerprint, NormalizedEvidenceContract: document.NormalizedEvidenceContractV1, UnitKind: document.EvidenceUnitPage})
		require.NoError(t, err)
		staged.Build.MarkdownChecksum, staged.Build.RenditionChecksum = rendition.MarkdownChecksum, rendition.Checksum
		staged.Build.Artifacts[1].BlobHash, staged.Build.Artifacts[1].Checksum, staged.Build.Artifacts[1].Size = rendition.MarkdownChecksum, rendition.MarkdownChecksum, int64(len(rendition.Markdown))
		staged.Artifacts[1].Payload = bytes.NewReader(rendition.Markdown)
		for i, artifact := range staged.Build.Artifacts {
			written, e := f.blobs.WriteDetailedContext(t.Context(), staged.Artifacts[i].Payload)
			require.NoError(t, e)
			require.Equal(t, artifact.BlobHash, written.Hash)
			require.NoError(t, f.catalog.RecordRenditionBlob(t.Context(), written.Hash, written.Size, processingBlobPhysical(t, written)))
		}
		require.NoError(t, f.catalog.StageRenditionBuild(t.Context(), staged.Build))
		generation, err := f.catalog.StageLexicalGeneration(t.Context(), staged.LexicalGenerationID)
		require.NoError(t, err)
		require.NoError(t, f.catalog.PublishRenditionAndLexicalHeads(t.Context(), staged.Attachment, staged.Head, generation.ID))
		node, err := f.catalog.NodeByPath(t.Context(), "/source.pdf")
		require.NoError(t, err)
		replacement, err := f.blobs.WriteDetailedContext(t.Context(), strings.NewReader("synthetic replacement"))
		require.NoError(t, err)
		current, _, err := f.catalog.ReplaceContent(t.Context(), node.ID, node.Revision, replacement.Hash, replacement.Size, "application/pdf", processingBlobPhysical(t, replacement))
		require.NoError(t, err)
		member := bundle.Member{NodeID: node.ID, VersionID: f.versionID, SHA256: node.BlobHash, Size: node.Size, Revision: current.Revision}
		source, err := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
		require.NoError(t, err)
		req := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: f.profile.Fingerprint, AttachmentID: staged.Attachment.ID, BuildID: staged.Build.ID}}
		before, err := f.blobs.List()
		require.NoError(t, err)
		time.Sleep(9 * time.Minute)
		gate := newTestOperationGate()

		pruned := false
		publish := func(ctx context.Context, fn func() error) error {
			time.Sleep(2 * time.Minute)
			require.NoError(t, f.catalog.CleanupExportAuthority(ctx))
			_, e := f.catalog.PruneContentVersions(ctx, node.ID, current.Revision, store.VersionPruneSelector{VersionIDs: []string{f.versionID}}, true)
			require.NoError(t, e)
			pruned = true
			return fn()
		}
		_, err = PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "https://archive.example.invalid", publish, gate.MaintainContext)
		require.True(t, pruned)
		require.ErrorIs(t, err, store.ErrNotFound)
		_, versionErr := f.catalog.ContentVersionByID(t.Context(), f.versionID)
		require.ErrorIs(t, versionErr, store.ErrNotFound)
		after, e := f.blobs.List()
		require.NoError(t, e)
		require.Equal(t, before, after)
		require.NoError(t, filepath.WalkDir(filepath.Dir(f.databasePath), func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			require.False(t, strings.HasPrefix(d.Name(), ".export-okf-"), path)
			return nil
		}))
	})
}

func TestReviewOKFCleanupFailureAfterSuccessfulSeal(t *testing.T) {
	t.Parallel()
	f := newPublicationFixture(t)
	provider := newWorkerProvider(t)
	profile := workerProcessingProfile(t, provider.Descriptor())
	request := workerJobRequest(f.versionID, profile, provider.Descriptor())
	grantWorkerConsent(t, f.catalog, request)
	_, _, err := f.catalog.EnqueueRenditionJob(t.Context(), request)
	require.NoError(t, err)
	now := time.Now().UTC()
	gate := newTestOperationGate()
	worker, err := NewRenditionWorker(RenditionWorkerConfig{Catalog: f.catalog, Blobs: f.blobs, Runtime: workerRuntime{provider: provider}, Gate: gate, Owner: "okf-worker-test", LeaseDuration: time.Minute, IdleDelay: time.Millisecond, Clock: func() time.Time { return now }})
	require.NoError(t, err)
	_, err = worker.RunOne(t.Context())
	require.NoError(t, err)
	view, err := f.catalog.ActiveRendition(t.Context(), f.versionID, profile.Fingerprint)
	require.NoError(t, err)
	v, _ := f.catalog.ContentVersionByID(t.Context(), f.versionID)
	node, _ := f.catalog.NodeByID(t.Context(), v.NodeID)
	member := bundle.Member{NodeID: node.ID, VersionID: f.versionID, SHA256: v.BlobHash, Size: v.Size, Revision: node.Revision}
	source, err := f.catalog.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	req := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: view.Attachment.ID, BuildID: view.Build.ID}}
	calls := 0
	injected := errors.New("maintenance lease unavailable")
	plan, err := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "https://archive.example.invalid", gate.MutateContext, func(context.Context, func() error) error { calls++; return injected })
	t.Logf("maintain calls on success path=%d err=%v planID=%q", calls, err, plan.ID)
	_, found, rerr := f.catalog.ExportPlanReplay(t.Context(), "owner", req)
	t.Logf("plan sealed=%v %v", found, rerr)
	require.NoError(t, err, "successful seal must not fail because of cleanup")
	release, err := f.catalog.AcquirePhotoExportPreparation(t.Context())
	require.NoError(t, err)
	defer release()
	replayed, err := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, plan, replayed)
}

func TestOKFMissingOriginDoesNotWaitForPreparationSlot(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newPublicationFixture(t)
		release, err := f.catalog.AcquirePhotoExportPreparation(t.Context())
		require.NoError(t, err)
		hash := strings.Repeat("a", 64)
		request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: uuid.New().String(), MemberHash: hash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: hash, AttachmentID: hash, BuildID: hash}}
		result := make(chan error, 1)
		go func() {
			_, e := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", request, "", nil, nil)
			result <- e
		}()
		synctest.Wait()
		var got error
		select {
		case got = <-result:
		default:
			got = errors.New("blocked on preparation slot")
		}
		release()
		require.ErrorIs(t, got, ErrOKFOriginUnavailable)
	})
}
