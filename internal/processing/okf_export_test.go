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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/bundle"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/kit/pack"
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

	invalid bool
}

func (s *okfTestStream) Verified() bool {
	return !s.invalid && s.instrumentedVerifiedStream.Verified()
}

func (s *okfTestStream) Read(p []byte) (int, error) {
	n, err := s.instrumentedVerifiedStream.Read(p)
	if errors.Is(err, io.EOF) {
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
			reader := &okfCountingReader{stream: &okfTestStream{instrumentedVerifiedStream: stream, invalid: tc.name == "tampered"}, size: tc.size}
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
	reader = &okfCountingReader{stream: &okfCancelStream{okfTestStream: &okfTestStream{instrumentedVerifiedStream: underlying}, cancel: stopMid}, size: int64(len(data))}
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
}

func TestOKFReceiptVariants(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, provider, source string
		receiptJSON            string
		metadataMismatch       bool
		tampered               bool
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
		{name: "malformed fields", provider: "invalid provider"},
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
				require.Nil(t, receipt.Generated)
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

func TestOKFRejectsLegacyRendition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, mediaType, content string }{
		{"empty PDF", "application/pdf", ""},
		{"empty text", "text/plain", ""},
		{"unenveloped PDF", "application/pdf", "Legacy text"},
	} {
		t.Run(tc.name, func(t *testing.T) { testOKFRejectsLegacy(t, tc.mediaType, tc.content) })
	}
}

func testOKFRejectsLegacy(t *testing.T, mediaType, content string) {
	t.Helper()
	f := newPublicationFixture(t)
	original, err := f.catalog.NodeByPath(t.Context(), "/source.pdf")
	require.NoError(t, err)
	node, err := f.catalog.CreateFile(t.Context(), f.catalog.RootID(), "synthetic.txt", original.BlobHash, original.Size, mediaType)
	require.NoError(t, err)
	staged := f.stageForSource(t, publicationIDs{"nonpdf-build", "nonpdf-attachment", "nonpdf-generation"}, "synthetic text", "Synthetic heading", node.CurrentVersionID, node.BlobHash)
	empty, err := f.blobs.WriteDetailedContext(t.Context(), strings.NewReader(content))
	require.NoError(t, err)
	staged.Build.Artifacts[1].BlobHash, staged.Build.Artifacts[1].Checksum, staged.Build.Artifacts[1].Size = empty.Hash, empty.Hash, empty.Size
	staged.Build.MarkdownChecksum = empty.Hash
	if content == "" {
		staged.Build.LexicalSegments = nil
	}
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
	require.ErrorContains(t, err, "reprocess")
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
	})
}

func TestOKFCleanupSkippedAfterSuccessfulSeal(t *testing.T) {
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
	var plan bundle.Plan
	t.Run("successful seal skips cleanup", func(t *testing.T) {
		calls := 0
		injected := errors.New("maintenance lease unavailable")
		var err error
		plan, err = PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "https://archive.example.invalid", gate.MutateContext, func(context.Context, func() error) error { calls++; return injected })
		require.NoError(t, err, "successful seal must not fail because of cleanup")
		require.Zero(t, calls, "successful seal must skip cleanup")
	})
	t.Run("replay bypasses origin and occupied slot", func(t *testing.T) {
		release, err := f.catalog.AcquirePhotoExportPreparation(t.Context())
		require.NoError(t, err)
		defer release()
		replayed, err := PrepareOKFExportPlan(t.Context(), f.catalog, f.blobs, "owner", req, "", nil, nil)
		require.NoError(t, err)
		require.Equal(t, plan, replayed)
	})
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

//nolint:paralleltest // The pack.SyncDir fault seam is process-wide.
func TestOKFFailedSealReclaimsNewBlob(t *testing.T) {
	f := newPublicationFixture(t)
	provider := newWorkerProvider(t)
	profile := workerProcessingProfile(t, provider.Descriptor())
	jobRequest := workerJobRequest(f.versionID, profile, provider.Descriptor())
	grantWorkerConsent(t, f.catalog, jobRequest)
	_, _, err := f.catalog.EnqueueRenditionJob(t.Context(), jobRequest)
	require.NoError(t, err)
	now := time.Now().UTC()
	gate := newTestOperationGate()
	worker, err := NewRenditionWorker(RenditionWorkerConfig{Catalog: f.catalog, Blobs: f.blobs, Runtime: workerRuntime{provider: provider}, Gate: gate, Owner: "okf-cleanup-test", LeaseDuration: time.Minute, IdleDelay: time.Millisecond, Clock: func() time.Time { return now }})
	require.NoError(t, err)
	_, err = worker.RunOne(t.Context())
	require.NoError(t, err)
	view, err := f.catalog.ActiveRendition(t.Context(), f.versionID, profile.Fingerprint)
	require.NoError(t, err)
	node, err := f.catalog.NodeByPath(t.Context(), "/source.pdf")
	require.NoError(t, err)
	hash, size := node.BlobHash, node.Size
	s := struct {
		*store.Store

		Blobs    *blob.Store
		BlobsDir string
	}{f.catalog, f.blobs, filepath.Join(filepath.Dir(f.databasePath), "blobs")}
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: hash, Size: size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: view.Attachment.ID, BuildID: view.Build.ID}}
	files := func() []string {
		var out []string
		require.NoError(t, filepath.WalkDir(s.BlobsDir, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				out = append(out, path)
			}
			return nil
		}))
		return out
	}
	passthrough := func(_ context.Context, fn func() error) error { return fn() }
	// Read an independently sealed output, then substitute only the origin for
	// the distinct candidate. This avoids reconstructing the renderer header.
	expectedOutput := func(ctx context.Context, req bundle.PlanRequest, origin string) []byte {
		seed := req
		seed.OperationID = uuid.New().String()
		const seedOrigin = "https://seed.example.invalid"
		plan, e := PrepareOKFExportPlan(ctx, s.Store, s.Blobs, "owner", seed, seedOrigin, passthrough, passthrough)
		require.NoError(t, e)
		var hash string
		require.NoError(t, s.WalkExportDocuments(ctx, plan.ID, func(d bundle.Document) error { hash = d.Roles[0].SHA256; return nil }))
		stream, _, e := s.Blobs.OpenStreamContext(ctx, hash)
		require.NoError(t, e)
		content, e := io.ReadAll(stream)
		require.NoError(t, e)
		require.NoError(t, stream.Close())
		require.Contains(t, string(content), seedOrigin)
		return bytes.Replace(content, []byte(seedOrigin), []byte(origin), 1)
	}
	syncOutput := expectedOutput(t.Context(), request, "https://failed-sync.example.invalid")
	before := files()
	injected := errors.New("synthetic publish failure")
	t.Run("publish failure and cancellation", func(t *testing.T) {
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://failure.example.invalid", func(context.Context, func() error) error { return injected }, passthrough)
		require.ErrorIs(t, err, injected)
		require.Equal(t, before, files())
		interrupted, stop := context.WithCancel(t.Context())
		_, err = PrepareOKFExportPlan(interrupted, s.Store, s.Blobs, "owner", request, "https://failure.example.invalid", func(_ context.Context, fn func() error) error { stop(); return fn() }, passthrough)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, before, files())
	})
	originalSync := pack.SyncDir
	t.Run("sync failure", func(t *testing.T) {
		syncFailure := errors.New("synthetic shard sync failure")
		writeFail := func(_ context.Context, fn func() error) error {
			digest := sha256.Sum256(syncOutput)
			hash := hex.EncodeToString(digest[:])
			shard := filepath.Join(s.BlobsDir, hash[:2])
			require.NoError(t, os.MkdirAll(shard, 0700))
			pack.SyncDir = func(path string) error {
				if path == shard {
					return syncFailure
				}
				return originalSync(path)
			}
			defer func() { pack.SyncDir = originalSync }()
			return fn()
		}
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://failed-sync.example.invalid", writeFail, passthrough)
		require.ErrorIs(t, err, syncFailure)
		require.Equal(t, before, files(), "failed durable write must not leave a new loose blob")
	})
	// A competing seal wins after admission, with bytes distinct from this candidate.
	for _, failCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "competing seal", true: "competing seal cleanup failure"}[failCleanup], func(t *testing.T) {
			candidateRequest := request
			candidateRequest.OperationID = uuid.New().String()
			candidate := expectedOutput(t.Context(), candidateRequest, "https://candidate.example.invalid")
			var winner bundle.Plan
			var candidateHash, winnerHash string
			cleanupFailure := errors.New("synthetic cleanup failure")
			publishWinner := func(ctx context.Context, fn func() error) error {
				digest := sha256.Sum256(candidate)
				candidateHash = hex.EncodeToString(digest[:])
				winnerBytes := bytes.Replace(candidate, []byte("candidate.example.invalid"), []byte("winner.example.invalid"), 1)
				require.NotEqual(t, candidate, winnerBytes)
				written, e := s.Blobs.WriteDetailedContext(ctx, bytes.NewReader(winnerBytes))
				require.NoError(t, e)
				winnerHash = written.Hash
				encoding, e := written.EncodingName()
				require.NoError(t, e)
				input, e := s.ExportOKFInput(ctx, "owner", candidateRequest)
				require.NoError(t, e)
				tags := make([]string, 0, len(input.Tags))
				for _, tag := range input.Tags {
					tags = append(tags, tag.Name)
				}
				receipt := bundle.OKFReceipt{Version: 1, Source: input.Member, NodeRevision: input.Member.Revision, Option: *candidateRequest.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: "https://winner.example.invalid/api/v1/versions/" + input.Member.VersionID + "/content", Tags: tags, Generated: input.Generated}
				prepared := store.PreparedOKFExport{Input: input, Receipt: receipt, SHA256: written.Hash, Size: written.Size, Physical: store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created}}
				var own bool
				winner, own, e = s.SealOKFExportPlan(ctx, "owner", candidateRequest, prepared)
				require.NoError(t, e)
				require.True(t, own)
				return fn()
			}
			maintain := passthrough
			if failCleanup {
				maintain = func(context.Context, func() error) error { return cleanupFailure }
			}
			result, e := PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", candidateRequest, "https://candidate.example.invalid", publishWinner, maintain)
			require.Equal(t, winner.ID, result.ID)
			if failCleanup {
				require.ErrorIs(t, e, cleanupFailure)
				require.NoError(t, s.Blobs.Remove(candidateHash))
			} else {
				require.NoError(t, e)
				_, _, e = s.Blobs.OpenStreamContext(t.Context(), candidateHash)
				require.Error(t, e)
			}
			stream, _, e := s.Blobs.OpenStreamContext(t.Context(), winnerHash)
			require.NoError(t, e)
			_, e = io.Copy(io.Discard, stream)
			require.NoError(t, e)
			require.NoError(t, stream.Close())
		})
	}
	t.Run("serialized replay", func(t *testing.T) {
		replayRequest := request
		replayRequest.OperationID = uuid.New().String()
		entered, release := make(chan struct{}), make(chan struct{})
		type outcome struct {
			plan bundle.Plan
			err  error
		}
		outcomes := make(chan outcome, 2)
		var publications atomic.Int32
		firstPublish := func(_ context.Context, fn func() error) error {
			publications.Add(1)
			close(entered)
			<-release
			return fn()
		}
		secondPublish := func(_ context.Context, fn func() error) error { publications.Add(1); return fn() }
		go func() {
			p, e := PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", replayRequest, "https://replay.example.invalid", firstPublish, passthrough)
			outcomes <- outcome{p, e}
		}()
		<-entered
		started := make(chan struct{})
		go func() {
			close(started)
			p, e := PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", replayRequest, "https://replay.example.invalid", secondPublish, passthrough)
			outcomes <- outcome{p, e}
		}()
		<-started
		close(release)
		first, second := <-outcomes, <-outcomes
		require.NoError(t, first.err)
		require.NoError(t, second.err)
		require.Equal(t, first.plan, second.plan)
		require.EqualValues(t, 1, publications.Load())
	})
	t.Run("cancellation after rename", func(t *testing.T) {
		// Inject cancellation precisely after the loose rename and before shard sync.
		cancelCtx, cancelAfterRename := context.WithCancel(t.Context())
		cancelOutput := expectedOutput(t.Context(), request, "https://cancel-after-rename.example.invalid")
		beforeCancel := files()
		cancelPublish := func(_ context.Context, fn func() error) error {
			sum := sha256.Sum256(cancelOutput)
			h := hex.EncodeToString(sum[:])
			shard := filepath.Join(s.BlobsDir, h[:2])
			require.NoError(t, os.MkdirAll(shard, 0700))
			pack.SyncDir = func(path string) error {
				if path == shard {
					cancelAfterRename()
					return context.Canceled
				}
				return originalSync(path)
			}
			defer func() { pack.SyncDir = originalSync }()
			return fn()
		}
		_, err = PrepareOKFExportPlan(cancelCtx, s.Store, s.Blobs, "owner", request, "https://cancel-after-rename.example.invalid", cancelPublish, passthrough)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, beforeCancel, files())
	})
	t.Run("root survival and prune", func(t *testing.T) {
		existingRequest := request
		existingRequest.OperationID = uuid.New().String()
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", existingRequest, "https://existing.example.invalid", passthrough, passthrough)
		require.NoError(t, err)
		existing := files()
		retry := request
		retry.OperationID = uuid.New().String()
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", retry, "https://existing.example.invalid", func(_ context.Context, fn func() error) error {
			if e := fn(); e != nil {
				return e
			}
			return injected
		}, passthrough)
		require.NoError(t, err, "a committed seal survives a later publish-wrapper error")
		require.Equal(t, existing, files(), "a previously rooted identical blob must survive")
		before = files()
		publish := func(ctx context.Context, fn func() error) error {
			_, _, e := s.Move(ctx, node.ID, s.RootID(), "renamed.pdf", node.Revision)
			if e != nil {
				return e
			}
			return fn()
		}
		maintain := func(_ context.Context, fn func() error) error { return fn() }
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", request, "https://archive.example.invalid", publish, maintain)
		require.ErrorIs(t, err, bundle.ErrConflict)
		require.Equal(t, before, files())
		current, err := s.NodeByID(t.Context(), node.ID)
		require.NoError(t, err)
		member.Revision = current.Revision
		rootSource, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
		require.NoError(t, err)
		rootRequest := request
		rootRequest.OperationID, rootRequest.SourceID, rootRequest.MemberHash = uuid.New().String(), rootSource.ID, rootSource.MemberHash
		rootOutput := expectedOutput(t.Context(), rootRequest, "https://concurrent.example.invalid")
		var rootedHash string
		competingPublish := func(ctx context.Context, fn func() error) error {
			_, _, e := s.Move(ctx, current.ID, s.RootID(), "renamed-again.pdf", current.Revision)
			require.NoError(t, e)
			sealErr := fn()
			require.ErrorIs(t, sealErr, bundle.ErrConflict)
			require.NoError(t, s.Blobs.WithMutation(ctx, func() error {
				written, writeErr := s.Blobs.WriteDetailedContext(ctx, bytes.NewReader(rootOutput))
				if writeErr != nil {
					return writeErr
				}
				rootedHash = written.Hash
				encoding, encodingErr := written.EncodingName()
				if encodingErr != nil {
					return encodingErr
				}
				_, e = s.CreateFile(ctx, s.RootID(), "concurrent-original.md", written.Hash, written.Size, "text/markdown", store.BlobPhysical{Encoding: encoding, StoredBytes: written.StoredSize, PackEligible: written.PackEligible, MD5: written.MD5, Created: written.Created})
				return e
			}))
			return sealErr
		}
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", rootRequest, "https://concurrent.example.invalid", competingPublish, passthrough)
		require.ErrorIs(t, err, bundle.ErrConflict)
		require.NotEmpty(t, rootedHash)
		rooted, _, err := s.Blobs.OpenStreamContext(t.Context(), rootedHash)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, rooted)
		require.NoError(t, err)
		require.NoError(t, rooted.Close())
		current, err = s.NodeByID(t.Context(), node.ID)
		require.NoError(t, err)
		replacementHash, replacementSize, err := s.Blobs.WriteContext(t.Context(), strings.NewReader("synthetic replacement"))
		require.NoError(t, err)
		current, _, err = s.ReplaceContent(t.Context(), current.ID, current.Revision, replacementHash, replacementSize, "application/pdf")
		require.NoError(t, err)
		member.Revision = current.Revision
		oldSource, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
		require.NoError(t, err)
		pruneRequest := request
		pruneRequest.OperationID, pruneRequest.SourceID, pruneRequest.MemberHash = uuid.New().String(), oldSource.ID, oldSource.MemberHash
		attempted := false
		prune := func(ctx context.Context, fn func() error) error {
			_, e := s.PruneContentVersions(ctx, node.ID, current.Revision, store.VersionPruneSelector{VersionIDs: []string{node.CurrentVersionID}}, true)
			require.ErrorIs(t, e, bundle.ErrRetained)
			attempted = true
			return fn()
		}
		retainedPlan, err := PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", pruneRequest, "https://prune.example.invalid", prune, passthrough)
		require.NoError(t, err)
		require.True(t, attempted)
		retained, err := s.ContentVersionByID(t.Context(), node.CurrentVersionID)
		require.NoError(t, err)
		require.Equal(t, hash, retained.BlobHash)
		var docs []bundle.Document
		require.NoError(t, s.WalkExportDocuments(t.Context(), retainedPlan.ID, func(d bundle.Document) error { docs = append(docs, d); return nil }))
		require.Len(t, docs, 1)
		require.Equal(t, member, docs[0].Member)
		// A deduplicated, unreferenced blob belongs to an earlier writer and must survive.
		unrootedRequest := pruneRequest
		unrootedRequest.OperationID = uuid.New().String()
		unrootedBytes := expectedOutput(t.Context(), unrootedRequest, "https://unrooted.example.invalid")
		unrootedHash, _, err := s.Blobs.WriteContext(t.Context(), bytes.NewReader(unrootedBytes))
		require.NoError(t, err)
		unreferenced, err := s.OKFBlobUnreferenced(t.Context(), unrootedHash)
		require.NoError(t, err)
		require.True(t, unreferenced)
		_, err = PrepareOKFExportPlan(t.Context(), s.Store, s.Blobs, "owner", unrootedRequest, "https://unrooted.example.invalid", func(ctx context.Context, fn func() error) error {
			_, _, e := s.Move(ctx, current.ID, s.RootID(), "changed-final.pdf", current.Revision)
			require.NoError(t, e)
			return fn()
		}, passthrough)
		require.ErrorIs(t, err, bundle.ErrConflict)
		// Catalog-backed reads reject unreferenced blobs; a repeated verified write
		// proves the physical object remains and was deduplicated.
		retainedWrite, err := s.Blobs.WriteDetailedContext(t.Context(), bytes.NewReader(unrootedBytes))
		require.NoError(t, err)
		require.Equal(t, unrootedHash, retainedWrite.Hash)
		require.False(t, retainedWrite.Created)
	})
}
