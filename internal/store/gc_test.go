package store

import (
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/pack"
)

func TestPageLimitWithSentinelRejectsOverflow(t *testing.T) {
	_, err := pageLimitWithSentinel(math.MaxInt)
	require.ErrorContains(t, err, "too large")
	limit, err := pageLimitWithSentinel(10)
	require.NoError(t, err)
	assert.Equal(t, 11, limit)
}

func TestUnreachableBlobsPageBoundsCandidatesAcrossCatalogSizes(t *testing.T) {
	for _, liveCount := range []int{3, 300} {
		t.Run(fmt.Sprintf("live=%d", liveCount), func(t *testing.T) {
			s := newTestStore(t)
			ctx := t.Context()
			for i := range liveCount {
				_, err := s.CreateFile(ctx, s.RootID(), fmt.Sprintf("live-%03d", i),
					fmt.Sprintf("%064x", 1000+i), 1, "application/octet-stream")
				require.NoError(t, err)
			}
			want := []string{
				fmt.Sprintf("%064x", 10), fmt.Sprintf("%064x", 20),
				fmt.Sprintf("%064x", 30), fmt.Sprintf("%064x", 40),
				fmt.Sprintf("%064x", 50), fmt.Sprintf("%064x", 60),
				fmt.Sprintf("%064x", 70),
			}
			for _, hash := range want {
				_, err := s.db.ExecContext(ctx,
					`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
					hash, "2026-01-01T00:00:00Z")
				require.NoError(t, err)
			}

			var got []string
			var after *string
			for {
				page, err := s.UnreachableBlobsPageFrom(ctx, after, 3)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Examined, 3)
				for _, candidate := range page.Items {
					got = append(got, candidate.Hash)
				}
				if !page.More {
					break
				}
				require.Positive(t, page.Examined)
				after = &page.HighWater
			}
			assert.Equal(t, want, got)
		})
	}
}

func TestUnreachableBlobsRetainsCatalogedRenditionArtifacts(t *testing.T) {
	// Mutation caught: checking only content_versions would let generic blob GC
	// revoke live normalized-evidence and sanitized-Markdown blob authority.
	s, _ := newRenditionCatalogFixture(t)
	build := catalogRenditionBuild(s, catalogProcessingProfile(t, false))
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))

	unreachable, err := s.UnreachableBlobs(t.Context())
	require.NoError(t, err)
	assert.Empty(t, unreachable)

	page, err := s.UnreachableBlobsPageFrom(t.Context(), nil, 10)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestUnreachableBlobsRetainsStagedBuildSourcesWithoutVersionAttachment(t *testing.T) {
	// Mutation caught: a staged provider build is backup authority even when its
	// original version was pruned; generic blob GC must not collect that exact
	// source out from under validation or backup closure.
	s, _ := newRenditionCatalogFixture(t)
	ctx := t.Context()
	stagedSource := fakeHash("17")
	require.NoError(t, s.withStorageTx(ctx, func(tx *sql.Tx) error {
		return s.EnsureBlobTx(tx, stagedSource, 23)
	}))
	build := catalogRenditionBuild(s, catalogProcessingProfile(t, false))
	build.ID = catalogBuildReplacement
	build.SourceSHA256 = stagedSource
	require.NoError(t, s.StageRenditionBuild(ctx, build))

	unreachable, err := s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	assert.Empty(t, unreachable)
	page, err := s.UnreachableBlobsPageFrom(ctx, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestDerivativeGCPlanSelectsOnlyUnrootedStagedBuilds(t *testing.T) {
	// Mutation caught: treating staged immutable builds as permanently live
	// would leak their artifacts, units, segments, and provider receipts.
	s, versions := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))

	plan, err := s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	require.Len(t, plan.Builds, 1)
	assert.Equal(t, build.ID, plan.Builds[0].BuildID)
	assert.Equal(t, []string{catalogMarkdownBlobHash, catalogEvidenceBlobHash},
		plan.Builds[0].ArtifactBlobHashes)

	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(),
		ContentVersionID: versions[0], BuildID: build.ID, Profile: profile,
		AttachedAt: "2026-08-22T10:00:00.000000000Z",
	}
	require.NoError(t, s.AttachRenditionBuild(t.Context(), attachment))
	plan, err = s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	assert.Empty(t, plan.Builds, "a version attachment roots its shared build")
}

func TestDerivativeGCPlanHonorsEveryTypedBuildRootAndFencing(t *testing.T) {
	// Mutation caught: omitting any current producer class, accepting a stale
	// release, or treating an expired lease as live can collect an exact build
	// that active work still requires or leak one after a crash.
	s, _ := newRenditionCatalogFixture(t)
	build := catalogRenditionBuild(s, catalogProcessingProfile(t, false))
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))

	for index, kind := range []CurrentRenditionRootKind{
		RenditionRootRetention,
		RenditionRootAudit,
		RenditionRootJob,
		RenditionRootReaderLease,
		RenditionRootWorkerLease,
		RenditionRootBackupPin,
	} {
		t.Run(string(kind), func(t *testing.T) {
			root := CurrentRenditionRoot{
				ID: fmt.Sprintf("root-%d", index), Kind: kind,
				TargetKind: RenditionRootBuild, TargetID: build.ID,
				FencingToken: 7, RecordedAt: "2026-08-23T10:00:00.000000000Z",
			}
			if kind == RenditionRootReaderLease || kind == RenditionRootWorkerLease {
				root.ExpiresAt = "2099-08-23T10:05:00.000000000Z"
			}
			require.NoError(t, s.PutCurrentRenditionRoot(t.Context(), root))
			plan, err := s.DerivativeGCPlan(t.Context())
			require.NoError(t, err)
			assert.Empty(t, plan.Builds)

			released, err := s.ReleaseCurrentRenditionRoot(t.Context(), root.ID, 6)
			require.NoError(t, err)
			assert.False(t, released, "a stale worker must not release a renewed root")
			plan, err = s.DerivativeGCPlan(t.Context())
			require.NoError(t, err)
			assert.Empty(t, plan.Builds)

			released, err = s.ReleaseCurrentRenditionRoot(t.Context(), root.ID, 7)
			require.NoError(t, err)
			assert.True(t, released)
		})
	}

	expired := CurrentRenditionRoot{
		ID: "expired-worker", Kind: RenditionRootWorkerLease,
		TargetKind: RenditionRootBuild, TargetID: build.ID,
		FencingToken: 8, RecordedAt: "2020-08-23T10:00:00.000000000Z",
		ExpiresAt: "2020-08-23T10:05:00.000000000Z",
	}
	require.NoError(t, s.PutCurrentRenditionRoot(t.Context(), expired))
	plan, err := s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	require.Len(t, plan.Builds, 1)
	assert.Equal(t, []string{"expired-worker"}, plan.ExpiredRootIDs)
}

func TestPurgeDerivativesRetainsActiveWorkerThenCollectsExpiredFence(t *testing.T) {
	// Mutation caught: ignoring an active worker lease can remove its exact
	// staged build during maintenance; retaining an expired renewed fence leaks
	// a crashed worker's complete manifest forever.
	s, _ := newRenditionCatalogFixture(t)
	ctx := t.Context()
	build := catalogRenditionBuild(s, catalogProcessingProfile(t, false))
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	root := CurrentRenditionRoot{
		ID: "worker-claim-17", Kind: RenditionRootWorkerLease,
		TargetKind: RenditionRootBuild, TargetID: build.ID,
		FencingToken: 17, RecordedAt: "2026-08-23T10:00:00.000000000Z",
		ExpiresAt: "2099-08-23T10:05:00.000000000Z",
	}
	require.NoError(t, s.PutCurrentRenditionRoot(ctx, root))

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{})
	require.NoError(t, err)
	assert.Zero(t, report.RemovedBuilds)
	assert.Empty(t, report.RetainedBuildIDs,
		"ordinary collection does not report an unselected retained build")

	root.FencingToken = 18
	root.RecordedAt = "2026-08-23T10:01:00.000000000Z"
	root.ExpiresAt = "2020-08-23T10:05:00.000000000Z"
	require.NoError(t, s.PutCurrentRenditionRoot(ctx, root))
	released, err := s.ReleaseCurrentRenditionRoot(ctx, root.ID, 17)
	require.NoError(t, err)
	assert.False(t, released, "stale completion cannot release a renewed worker fence")

	report, err = s.PurgeDerivatives(ctx, PurgeRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.ExpiredRootsRemoved)
	assert.Equal(t, 1, report.RemovedBuilds)
}

func TestDerivativeGCPlanPinsExactActiveAndLeasedLexicalGenerations(t *testing.T) {
	// Mutation caught: following only the current lexical head would reclaim an
	// old exact generation while a reader still has it pinned.
	s, versions := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	first, err := s.StageLexicalGeneration(t.Context(), fakeHash("81"))
	require.NoError(t, err)

	plan, err := s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{first.ID}, plan.LexicalGenerations,
		"an unreachable staged generation is collectible")

	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(),
		ContentVersionID: versions[0], BuildID: build.ID, Profile: profile,
		AttachedAt: "2026-08-23T11:00:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(t.Context(), attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T11:01:00.000000000Z",
		}, first.ID))
	lease, err := s.AcquireLexicalGeneration(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release()) })

	second, err := s.StageLexicalGeneration(t.Context(), fakeHash("82"))
	require.NoError(t, err)
	require.NoError(t, s.PublishRenditionAndLexicalHeads(t.Context(), attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T11:02:00.000000000Z",
		}, second.ID))

	plan, err = s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	assert.Empty(t, plan.LexicalGenerations,
		"the active head and exact old reader lease root both generations")
	require.NoError(t, lease.Release())
	plan, err = s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{first.ID}, plan.LexicalGenerations)
}

func TestDerivativeGCPlanRetainsBuildManifestThroughRootedGeneration(t *testing.T) {
	// Mutation caught: collecting an un-attached build while a backup-pinned
	// generation still contains its text would make the rooted projection's
	// manifest impossible to explain or restore.
	s, _ := newRenditionCatalogFixture(t)
	build := catalogRenditionBuild(s, catalogProcessingProfile(t, false))
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	generation, err := s.StageLexicalGeneration(t.Context(), fakeHash("83"))
	require.NoError(t, err)
	require.NoError(t, s.PutCurrentRenditionRoot(t.Context(), CurrentRenditionRoot{
		ID: "backup-capture-1", Kind: RenditionRootBackupPin,
		TargetKind: RenditionRootLexicalGeneration, TargetID: generation.ID,
		FencingToken: 1, RecordedAt: "2026-08-23T11:10:00.000000000Z",
	}))

	plan, err := s.DerivativeGCPlan(t.Context())
	require.NoError(t, err)
	assert.Empty(t, plan.Builds)
	assert.Empty(t, plan.LexicalGenerations)
}

func TestPurgeDerivativesRemovesCompleteLiveManifestButNeverOriginal(t *testing.T) {
	// Mutation caught: deleting only Markdown or attachments would leave
	// normalized evidence, provider artifacts, units, segments, FTS rows,
	// legacy cache text, or the immutable build receipt in the live vault.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("84"))
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(),
		ContentVersionID: versions[0], BuildID: build.ID, Profile: profile,
		AttachedAt: "2026-08-23T12:00:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(ctx, attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T12:01:00.000000000Z",
		}, generation.ID))
	require.NoError(t, s.RecordExtraction(ctx, ExtractionResult{
		BlobHash: catalogSourceHash, Extractor: "plain-text", ExtractorVersion: 1,
		Status: ExtractionOK, Text: "portable legacy sensitive text",
	}))

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: versions})
	require.NoError(t, err)
	assert.Equal(t, 1, report.RemovedHeads)
	assert.Equal(t, 1, report.RemovedAttachments)
	assert.Equal(t, 2, report.RemovedBuilds)
	assert.Equal(t, 2, report.RemovedArtifacts)
	assert.Equal(t, 2, report.RemovedUnits)
	assert.Equal(t, 2, report.RemovedLexicalSegments)
	assert.Equal(t, 2, report.RemovedLexicalGenerations)
	assert.Equal(t, 3, report.RemovedLexicalRows)
	assert.Equal(t, 1, report.RemovedLegacyCacheRows)
	assert.Equal(t, []string{catalogMarkdownBlobHash, catalogEvidenceBlobHash},
		report.PhysicalDerivativeBlobsPendingGC)
	assert.True(t, report.ImmutableBackupCopiesUntouched)

	for _, table := range []string{
		"rendition_heads", "rendition_attachments", "rendition_builds",
		"rendition_artifacts", "rendition_units", "rendition_lexical_segments",
		"rendition_lexical_heads", "rendition_lexical_generations",
		"rendition_lexical_generation_manifests", "rendition_lexical_fts",
		"extracted_text", "content_fts",
	} {
		var count int
		require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		assert.Zero(t, count, table)
	}
	_, err = s.BlobInfo(ctx, catalogSourceHash)
	require.NoError(t, err, "purging derivatives must never revoke original blob authority")
	unreachable, err := s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	require.Len(t, unreachable, 2)
	assert.Equal(t, []string{catalogMarkdownBlobHash, catalogEvidenceBlobHash},
		[]string{unreachable[0].Hash, unreachable[1].Hash})

	replayed, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: versions})
	require.NoError(t, err)
	assert.Equal(t, PurgeReport{ImmutableBackupCopiesUntouched: true}, replayed,
		"replaying a completed purge must be an idempotent no-op")
}

func TestPurgeDerivativesRetainsSharedBuildWhileAnyAttachmentRemains(t *testing.T) {
	// Mutation caught: treating attachment deletion as build ownership would
	// erase shared derivatives still authorized for another content version.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	secondProfile := catalogProcessingProfile(t, true)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	first := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T13:00:00.000000000Z",
	}
	second := RenditionAttachmentRecord{
		ID: catalogAttachmentSecond, VaultID: s.VaultID(), ContentVersionID: versions[1],
		BuildID: build.ID, Profile: secondProfile, AttachedAt: "2026-08-23T13:01:00.000000000Z",
	}
	require.NoError(t, s.AttachRenditionBuild(ctx, first))
	require.NoError(t, s.AttachRenditionBuild(ctx, second))
	require.NoError(t, s.PublishRenditionHead(ctx, RenditionHeadRecord{
		ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
		AttachmentID: first.ID, PublishedAt: "2026-08-23T13:02:00.000000000Z",
	}))
	require.NoError(t, s.PublishRenditionHead(ctx, RenditionHeadRecord{
		ContentVersionID: versions[1], ProcessingProfileFingerprint: secondProfile.Fingerprint,
		AttachmentID: second.ID, PublishedAt: "2026-08-23T13:03:00.000000000Z",
	}))

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.NoError(t, err)
	assert.Equal(t, 1, report.RemovedHeads)
	assert.Equal(t, 1, report.RemovedAttachments)
	assert.Zero(t, report.RemovedBuilds)
	assert.Equal(t, []string{build.ID}, report.RetainedBuildIDs)
	assert.Empty(t, report.PhysicalDerivativeBlobsPendingGC)
	_, err = s.ActiveRendition(ctx, versions[0], profile.Fingerprint)
	require.ErrorIs(t, err, ErrNotFound)
	active, err := s.ActiveRendition(ctx, versions[1], secondProfile.Fingerprint)
	require.NoError(t, err)
	assert.Equal(t, build.ID, active.Build.ID)
	unreachable, err := s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	assert.Empty(t, unreachable)
}

func TestPurgeDerivativesDefersExactLiveBackupPinWithoutClaimingRepositoryErasure(t *testing.T) {
	// Mutation caught: ignoring an active in-vault snapshot pin would collect
	// exact live bytes mid-capture; treating immutable repository copies as a
	// live root would make them mutable or claim erasure that never occurred.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("85"))
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T14:00:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(ctx, attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T14:01:00.000000000Z",
		}, generation.ID))
	require.NoError(t, s.PutCurrentRenditionRoot(ctx, CurrentRenditionRoot{
		ID: "live-snapshot", Kind: RenditionRootBackupPin,
		TargetKind: RenditionRootLexicalGeneration, TargetID: generation.ID,
		FencingToken: 11, RecordedAt: "2026-08-23T14:02:00.000000000Z",
	}))

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.NoError(t, err)
	assert.Equal(t, []string{build.ID}, report.RetainedBuildIDs)
	assert.Equal(t, []string{generation.ID}, report.RetainedLexicalGenerations)
	assert.Zero(t, report.RemovedBuilds)
	assert.Zero(t, report.RemovedLexicalGenerations)
	assert.True(t, report.ImmutableBackupCopiesUntouched)

	released, err := s.ReleaseCurrentRenditionRoot(ctx, "live-snapshot", 11)
	require.NoError(t, err)
	assert.True(t, released)
	report, err = s.PurgeDerivatives(ctx, PurgeRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.RemovedBuilds)
	assert.Equal(t, 1, report.RemovedLexicalGenerations)
}

func TestPurgeDerivativesDoesNotLetBuildRootImplicitlyPinLexicalGeneration(t *testing.T) {
	// Mutation caught: treating a root for one exact build as a root for every
	// projection containing it would leave selected sensitive lexical text live
	// even though no head, lease, job, or backup pin requires that generation.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("89"))
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T14:10:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(ctx, attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T14:11:00.000000000Z",
		}, generation.ID))
	require.NoError(t, s.PutCurrentRenditionRoot(ctx, CurrentRenditionRoot{
		ID: "exact-build-backup-pin", Kind: RenditionRootBackupPin,
		TargetKind: RenditionRootBuild, TargetID: build.ID,
		FencingToken: 1, RecordedAt: "2026-08-23T14:12:00.000000000Z",
	}))

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.NoError(t, err)
	assert.Equal(t, []string{build.ID}, report.RetainedBuildIDs)
	assert.Empty(t, report.RetainedLexicalGenerations)
	assert.Zero(t, report.RemovedBuilds)
	assert.Equal(t, 1, report.RemovedLexicalGenerations)
	_, err = s.ActiveLexicalGeneration(ctx)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPurgeDerivativesCannotCollectGenerationUnderActiveReader(t *testing.T) {
	// Mutation caught: planning outside the reader-root mutex would allow
	// maintenance to delete an exact generation between acquire and query.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("86"))
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T15:00:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(ctx, attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T15:01:00.000000000Z",
		}, generation.ID))
	lease, err := s.AcquireLexicalGeneration(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release()) })

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.NoError(t, err)
	assert.Equal(t, []string{generation.ID}, report.RetainedLexicalGenerations)
	assert.Equal(t, []string{build.ID}, report.RetainedBuildIDs)
	assert.Zero(t, report.RemovedBuilds)
	assert.Zero(t, report.RemovedLexicalGenerations)
	assert.Equal(t, generation.ID, lease.Generation.ID)

	require.NoError(t, lease.Release())
	report, err = s.PurgeDerivatives(ctx, PurgeRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.RemovedBuilds)
	assert.Equal(t, 1, report.RemovedLexicalGenerations)
}

func TestPurgeDerivativesRollsBackEveryAuthorityOnManifestFailure(t *testing.T) {
	// Mutation caught: deleting heads or lexical rows outside the catalog
	// transaction would leave a failed purge partially authoritative.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("87"))
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T16:00:00.000000000Z",
	}
	require.NoError(t, s.PublishRenditionAndLexicalHeads(ctx, attachment,
		RenditionHeadRecord{
			ContentVersionID: versions[0], ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachment.ID, PublishedAt: "2026-08-23T16:01:00.000000000Z",
		}, generation.ID))
	_, err = s.db.Exec(`CREATE TRIGGER fail_derivative_manifest_purge
		BEFORE DELETE ON rendition_artifacts BEGIN
		SELECT RAISE(ABORT, 'synthetic derivative purge failure'); END`)
	require.NoError(t, err)

	_, err = s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.ErrorContains(t, err, "synthetic derivative purge failure")
	active, err := s.ActiveRendition(ctx, versions[0], profile.Fingerprint)
	require.NoError(t, err)
	assert.Equal(t, build.ID, active.Build.ID)
	activeGeneration, err := s.ActiveLexicalGeneration(ctx)
	require.NoError(t, err)
	assert.Equal(t, generation.ID, activeGeneration.ID)
	var builds, artifacts, attachments, heads, generations, lexicalRows int
	require.NoError(t, s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM rendition_builds),
		(SELECT COUNT(*) FROM rendition_artifacts),
		(SELECT COUNT(*) FROM rendition_attachments),
		(SELECT COUNT(*) FROM rendition_heads),
		(SELECT COUNT(*) FROM rendition_lexical_generations),
		(SELECT COUNT(*) FROM rendition_lexical_fts)`).Scan(
		&builds, &artifacts, &attachments, &heads, &generations, &lexicalRows))
	assert.Equal(t, []int{1, 2, 1, 1, 1, 1},
		[]int{builds, artifacts, attachments, heads, generations, lexicalRows})
}

func TestPurgeDerivativesCannotBypassActiveAuditAuthority(t *testing.T) {
	// Mutation caught: using a storage-only transaction for live purge would
	// create an unaudited privileged path that deletes catalog authority while
	// the vault's audited logical-mutation contract is active.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	require.NoError(t, s.AttachRenditionBuild(ctx, RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T16:10:00.000000000Z",
	}))
	seedInitialAuditAuthority(t, s, s.RootID())

	_, err := s.PurgeDerivatives(ctx, PurgeRequest{ContentVersionIDs: []string{versions[0]}})
	require.ErrorIs(t, err, ErrAuditMutationUnsupported)
	_, err = s.ActiveRendition(ctx, versions[0], profile.Fingerprint)
	require.ErrorIs(t, err, ErrNotFound, "the fixture attachment was staged, not headed")
	var builds, attachments int
	require.NoError(t, s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM rendition_builds),
		(SELECT COUNT(*) FROM rendition_attachments)`).Scan(&builds, &attachments))
	assert.Equal(t, []int{1, 1}, []int{builds, attachments})
}

func TestPurgeDerivativesCollectsUnheadedGenerationOverRetainedBuild(t *testing.T) {
	// Mutation caught: tying generation collection only to build deletion would
	// leak interrupted or superseded FTS projections whose builds stay live.
	s, versions := newRenditionCatalogFixture(t)
	ctx := t.Context()
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(ctx, build))
	require.NoError(t, s.AttachRenditionBuild(ctx, RenditionAttachmentRecord{
		ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0],
		BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-23T17:00:00.000000000Z",
	}))
	generation, err := s.StageLexicalGeneration(ctx, fakeHash("88"))
	require.NoError(t, err)

	report, err := s.PurgeDerivatives(ctx, PurgeRequest{})
	require.NoError(t, err)
	assert.Zero(t, report.RemovedBuilds)
	assert.Equal(t, 1, report.RemovedLexicalGenerations)
	assert.Equal(t, 1, report.RemovedLexicalRows)
	_, err = s.ActiveRendition(ctx, versions[0], profile.Fingerprint)
	require.ErrorIs(t, err, ErrNotFound, "the attachment is staged but not headed")
	var builds, generations int
	require.NoError(t, s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM rendition_builds),
		(SELECT COUNT(*) FROM rendition_lexical_generations)`).Scan(&builds, &generations))
	assert.Equal(t, 1, builds)
	assert.Zero(t, generations)
	assert.NotEmpty(t, generation.ID)
}

func TestBlobInventoryResumeQueriesUseIndexedHashRange(t *testing.T) {
	s := newTestStore(t)
	after := fmt.Sprintf("%064x", 900)
	tests := []struct {
		name        string
		query       func(*string, int) (string, []any)
		index       string
		rangeClause string
	}{
		{name: "verify inventory", query: blobHashesPageQuery,
			index: "sqlite_autoindex_blobs_1", rangeClause: "(hash>?)"},
		{name: "unreachable inventory", query: unreachableBlobScanQuery,
			index: "sqlite_autoindex_blobs_1", rangeClause: "(hash>?)"},
		{name: "pack mapping inventory", query: unreferencedMappingScanQuery,
			index: "blob_pack_entries_store_hash", rangeClause: "(store_id=? AND blob_hash>?)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := test.query(&after, 3)
			rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				details = append(details, detail)
			}
			require.NoError(t, rows.Err())
			plan := strings.Join(details, "\n")
			assert.Contains(t, plan, "SEARCH")
			assert.Contains(t, plan, test.index)
			assert.Contains(t, plan, test.rangeClause)
		})
	}
}

func TestBlobHashesPageNearEndUsesResumeKey(t *testing.T) {
	s := newTestStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	for i := range 5000 {
		_, err = tx.ExecContext(t.Context(),
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			fmt.Sprintf("%064x", i), "2026-01-01T00:00:00.000000000Z")
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	after := fmt.Sprintf("%064x", 4995)
	hashes, more, err := s.BlobHashesPageFrom(t.Context(), &after, 3)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Equal(t, []string{
		fmt.Sprintf("%064x", 4996),
		fmt.Sprintf("%064x", 4997),
		fmt.Sprintf("%064x", 4998),
	}, hashes)
}

func TestUnreachableBlobPageDistinguishesEmptyStoredKeyFromStart(t *testing.T) {
	s := newTestStore(t)
	later := "1000000000000000000000000000000000000000000000000000000000000000"
	for _, hash := range []string{"", later} {
		_, err := s.db.ExecContext(t.Context(),
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			hash, "2026-01-01T00:00:00.000000000Z")
		require.NoError(t, err)
	}

	first, err := s.UnreachableBlobsPageFrom(t.Context(), nil, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	assert.Empty(t, first.Items[0].Hash)
	assert.Empty(t, first.HighWater)
	require.True(t, first.More)

	after := first.HighWater
	second, err := s.UnreachableBlobsPageFrom(t.Context(), &after, 1)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Equal(t, later, second.Items[0].Hash)
	assert.False(t, second.More)
}

func TestUnreachableBlobScanBoundsExaminedLiveRun(t *testing.T) {
	for _, liveCount := range []int{8, 800} {
		t.Run(fmt.Sprintf("live=%d", liveCount), func(t *testing.T) {
			s := newTestStore(t)
			for i := 1; i <= liveCount; i++ {
				_, err := s.CreateFile(t.Context(), s.RootID(), fmt.Sprintf("live-%03d", i),
					fmt.Sprintf("%064x", i), 1, "application/octet-stream")
				require.NoError(t, err)
			}
			unreachable := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			_, err := s.db.ExecContext(t.Context(),
				`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
				unreachable, "2026-01-01T00:00:00.000000000Z")
			require.NoError(t, err)

			page, err := s.UnreachableBlobsPageFrom(t.Context(), nil, 4)
			require.NoError(t, err)
			assert.Empty(t, page.Items)
			assert.Equal(t, 4, page.Examined)
			assert.Equal(t, fmt.Sprintf("%064x", 4), page.HighWater)
			require.True(t, page.More)

			var got []string
			totalExamined := page.Examined
			for page.More {
				after := page.HighWater
				page, err = s.UnreachableBlobsPageFrom(t.Context(), &after, 4)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Examined, 4)
				totalExamined += page.Examined
				for _, candidate := range page.Items {
					got = append(got, candidate.Hash)
				}
			}
			assert.Equal(t, []string{unreachable}, got)
			assert.Equal(t, liveCount+1, totalExamined)
		})
	}
}

func TestUnreferencedMappingScanBoundsExaminedLiveRun(t *testing.T) {
	for _, liveCount := range []int{8, 800} {
		t.Run(fmt.Sprintf("live=%d", liveCount), func(t *testing.T) {
			s := newTestStore(t)
			packID := pack.NewPackID()
			addTestPack(t, s, packID, int64(liveCount+1), int64((liveCount+1)*5),
				"2026-01-01T00:00:00.000000000Z")
			tx, err := s.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			for i := 1; i <= liveCount; i++ {
				hash := fmt.Sprintf("%064x", i)
				_, err = tx.ExecContext(t.Context(),
					`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
					hash, "2026-01-01T00:00:00.000000000Z")
				require.NoError(t, err)
				_, err = tx.ExecContext(t.Context(), `
					INSERT INTO blob_pack_entries
						(blob_hash, store_id, pack_id, pack_offset, stored_len, raw_len, flags, crc32c)
					VALUES (?, ?, ?, ?, 5, 1, 0, 0)`, hash, s.primaryStoreID, packID,
					pack.MinEntryOffset+int64(i-1)*32)
				require.NoError(t, err)
			}
			dangling := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			_, err = tx.ExecContext(t.Context(), `
				INSERT INTO blob_pack_entries
					(blob_hash, store_id, pack_id, pack_offset, stored_len, raw_len, flags, crc32c)
				VALUES (?, ?, ?, ?, 5, 1, 0, 0)`, dangling, s.primaryStoreID, packID,
				pack.MinEntryOffset+int64(liveCount)*32)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())

			page, err := s.UnreferencedPackMappingsPage(t.Context(), nil, 4)
			require.NoError(t, err)
			assert.Empty(t, page.Items)
			assert.Equal(t, 4, page.Examined)
			assert.Equal(t, fmt.Sprintf("%064x", 4), page.HighWater)
			require.True(t, page.More)

			var got []string
			totalExamined := page.Examined
			for page.More {
				after := page.HighWater
				page, err = s.UnreferencedPackMappingsPage(t.Context(), &after, 4)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Examined, 4)
				totalExamined += page.Examined
				got = append(got, page.Items...)
			}
			assert.Equal(t, []string{dangling}, got)
			assert.Equal(t, liveCount+1, totalExamined)
		})
	}
}

func TestBlobPageUsesHashKeysetAcrossDeletionAndLowerInsertion(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	hash05 := fmt.Sprintf("%064x", 5)
	hash10 := fmt.Sprintf("%064x", 10)
	hash20 := fmt.Sprintf("%064x", 20)
	hash30 := fmt.Sprintf("%064x", 30)
	for _, hash := range []string{hash10, hash20, hash30} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			hash, "2026-01-01T00:00:00.000000000Z")
		require.NoError(t, err)
	}

	first, more, err := s.BlobsPage(ctx, "", 1)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, first, 1)
	assert.Equal(t, hash10, first[0].Hash)

	require.NoError(t, s.DeleteBlobRows(ctx, []string{hash10, hash20}))
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
		hash05, "2026-01-01T00:00:00Z")
	require.NoError(t, err)

	resumed, more, err := s.BlobsPage(ctx, hash10, 10)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, resumed, 1)
	assert.Equal(t, hash30, resumed[0].Hash,
		"a resumed cycle neither revisits deletions nor admits a new lower hash")

	fresh, more, err := s.BlobsPage(ctx, "", 10)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, fresh, 2)
	assert.Equal(t, []string{hash05, hash30}, []string{fresh[0].Hash, fresh[1].Hash})
}

func TestSparseRepackPageUsesCanonicalLiveHashKeyset(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for i, hash := range []string{
		fmt.Sprintf("%064x", 30), fmt.Sprintf("%064x", 10), fmt.Sprintf("%064x", 20),
	} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			hash, "2026-01-01T00:00:00Z")
		require.NoError(t, err)
		packID := pack.NewPackID()
		addTestPack(t, s, packID, 3, 30, "2026-01-01T00:00:00.000000000Z")
		addTestPackEntry(t, s, hash, packID, pack.MinEntryOffset+int64(i)*32, 5, 1)
	}

	first, more, err := s.SparseRepackPage(ctx, "", 2,
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, first, 2)
	assert.Equal(t, fmt.Sprintf("%064x", 10), first[0].Hash)
	assert.Equal(t, fmt.Sprintf("%064x", 20), first[1].Hash)

	second, more, err := s.SparseRepackPage(ctx, first[1].Hash, 2,
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, second, 1)
	assert.Equal(t, fmt.Sprintf("%064x", 30), second[0].Hash)
}

func TestUnreferencedPackMappingsPageIsCanonicalAndBounded(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	packID := pack.NewPackID()
	addTestPack(t, s, packID, 4, 40, "2026-01-01T00:00:00.000000000Z")
	for i, hash := range []string{
		fmt.Sprintf("%064x", 40), fmt.Sprintf("%064x", 10),
		fmt.Sprintf("%064x", 30), fmt.Sprintf("%064x", 20),
	} {
		addTestPackEntry(t, s, hash, packID, pack.MinEntryOffset+int64(i)*32, 5, 1)
	}

	first, err := s.UnreferencedPackMappingsPage(ctx, nil, 2)
	require.NoError(t, err)
	assert.True(t, first.More)
	assert.Equal(t, []string{fmt.Sprintf("%064x", 10), fmt.Sprintf("%064x", 20)}, first.Items)

	removed, err := s.DeleteUnreferencedPackMappings(ctx, first.Items)
	require.NoError(t, err)
	assert.Equal(t, int64(2), removed)
	second, err := s.UnreferencedPackMappingsPage(ctx, &first.HighWater, 2)
	require.NoError(t, err)
	assert.False(t, second.More)
	assert.Equal(t, []string{fmt.Sprintf("%064x", 30), fmt.Sprintf("%064x", 40)}, second.Items)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`, second.Items[0], nowRFC3339())
	require.NoError(t, err)
	removed, err = s.DeleteUnreferencedPackMappings(ctx, second.Items)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed, "a concurrently restored authority row protects its mapping")
}

func TestDeadPackUsagePageIsBounded(t *testing.T) {
	s := newTestStore(t)
	for range 4 {
		addTestPack(t, s, pack.NewPackID(), 1, 20, "2026-01-01T00:00:00.000000000Z")
	}

	page, more, err := s.DeadPackUsagePage(t.Context(), 3)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Len(t, page, 3)
}

func TestSparseRepackScanBoundsExaminedIneligiblePacks(t *testing.T) {
	for _, total := range []int{8, 800} {
		t.Run(fmt.Sprintf("packs=%d", total), func(t *testing.T) {
			s := newTestStore(t)
			for i := range total {
				hash := fmt.Sprintf("%064x", i+1)
				packID := pack.NewPackID()
				_, err := s.db.ExecContext(t.Context(),
					`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
					hash, "2026-01-01T00:00:00.000000000Z")
				require.NoError(t, err)
				addTestPack(t, s, packID, 1, 5, "2026-01-01T00:00:00.000000000Z")
				addTestPackEntry(t, s, hash, packID, pack.MinEntryOffset, 5, 1)
			}

			page, err := s.SparseRepackScanPage(t.Context(), "", "", 4,
				time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
			require.NoError(t, err)
			assert.Len(t, page.Items, 4)
			assert.True(t, page.More)
			for _, item := range page.Items {
				assert.False(t, item.Eligible)
			}
			continued, err := s.SparseRepackScanPage(t.Context(),
				page.Items[len(page.Items)-1].Hash, page.Items[len(page.Items)-1].Usage.PackID, 4,
				time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
			require.NoError(t, err)
			require.Len(t, continued.Items, 4)
			assert.NotEqual(t, page.Items[0].Usage.PackID, continued.Items[0].Usage.PackID)
			assert.Equal(t, total > 8, continued.More)
		})
	}
}

func TestSparseRepackScanIncludesExactlyHalfLiveEvenPack(t *testing.T) {
	s := newTestStore(t)
	hash := fmt.Sprintf("%064x", 1)
	packID := pack.NewPackID()
	_, err := s.db.ExecContext(t.Context(),
		`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
		hash, "2026-01-01T00:00:00.000000000Z")
	require.NoError(t, err)
	addTestPack(t, s, packID, 2, 10, "2026-01-01T00:00:00.000000000Z")
	addTestPackEntry(t, s, hash, packID, pack.MinEntryOffset, 5, 1)

	page, err := s.SparseRepackScanPage(t.Context(), "", "", 1,
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].Eligible,
		"one live entry in a two-entry pack is exactly half live")
}

func TestSparseRepackScanKeyStaysStableWhenEarlierPackLosesLiveness(t *testing.T) {
	s := newTestStore(t)
	packIDs := []string{pack.NewPackID(), pack.NewPackID()}
	slices.Sort(packIDs)
	hashes := []string{
		"1000000000000000000000000000000000000000000000000000000000000000",
		"2000000000000000000000000000000000000000000000000000000000000000",
	}
	for i, packID := range packIDs {
		_, err := s.db.ExecContext(t.Context(),
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			hashes[i], "2026-01-01T00:00:00.000000000Z")
		require.NoError(t, err)
		addTestPack(t, s, packID, 3, 30, "2026-01-01T00:00:00.000000000Z")
		addTestPackEntry(t, s, hashes[i], packID, pack.MinEntryOffset, 5, 1)
	}

	first, err := s.SparseRepackScanPage(t.Context(), "", "", 1,
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.True(t, first.More)
	assert.Equal(t, packIDs[0], first.Items[0].Usage.PackID)
	assert.Equal(t, hashes[0], first.Items[0].Hash)

	_, err = s.db.ExecContext(t.Context(), `DELETE FROM blobs WHERE hash = ?`, hashes[0])
	require.NoError(t, err)
	var retainedScanHash string
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`SELECT scan_hash FROM blob_packs WHERE pack_id = ?`, packIDs[0]).Scan(&retainedScanHash))
	assert.Equal(t, hashes[0], retainedScanHash)
	second, err := s.SparseRepackScanPage(t.Context(), first.Items[0].Hash,
		first.Items[0].Usage.PackID, 1,
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), time.Nanosecond, 1)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Equal(t, packIDs[1], second.Items[0].Usage.PackID,
		"the continuation key is immutable even when the earlier pack becomes dead")
}

func TestPackScanHashDoesNotChangeAfterPackCreation(t *testing.T) {
	s := newTestStore(t)
	packID := pack.NewPackID()
	originalHash := "8000000000000000000000000000000000000000000000000000000000000000"
	laterHash := "1000000000000000000000000000000000000000000000000000000000000000"
	for _, hash := range []string{originalHash, laterHash} {
		_, err := s.db.ExecContext(t.Context(),
			`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
			hash, "2026-01-01T00:00:00.000000000Z")
		require.NoError(t, err)
	}
	addTestPack(t, s, packID, 2, 10, "2026-01-01T00:00:00.000000000Z", originalHash)
	addTestPackEntry(t, s, originalHash, packID, pack.MinEntryOffset, 5, 1)
	addTestPackEntry(t, s, laterHash, packID, pack.MinEntryOffset+32, 5, 1)

	var scanHash string
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`SELECT scan_hash FROM blob_packs WHERE pack_id = ?`, packID).Scan(&scanHash))
	assert.Equal(t, originalHash, scanHash,
		"an established pack scan key remains immutable when mappings change")
}

func TestRepackSelectionQueriesUseSummaryIndexes(t *testing.T) {
	s := newTestStore(t)
	tests := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{name: "dead", query: deadPackUsagePageSQL, args: []any{2},
			wantIndex: "blob_packs_dead_scan"},
		{name: "sparse", query: sparseRepackScanPageSQL, args: []any{"", "", "", 2},
			wantIndex: "blob_packs_live_scan"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+test.query, test.args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				details = append(details, detail)
			}
			require.NoError(t, rows.Err())
			plan := strings.Join(details, "\n")
			assert.Contains(t, plan, test.wantIndex)
			assert.NotContains(t, plan, "blob_pack_entries")
			assert.NotContains(t, plan, "USE TEMP B-TREE")
		})
	}
}

func TestUnreachableBlobs(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	live, err := s.CreateFile(ctx, s.RootID(), "live.txt", fakeHash("a1"), 1, "text/plain")
	require.NoError(t, err)
	trashed, err := s.CreateFile(ctx, s.RootID(), "trashed.txt", fakeHash("b2"), 1, "text/plain")
	require.NoError(t, err)
	gone, err := s.CreateFile(ctx, s.RootID(), "gone.txt", fakeHash("c3"), 1, "text/plain")
	require.NoError(t, err)

	// Replacing the current pointer leaves the original version as a root.
	_, err = s.db.Exec(`INSERT INTO blobs (hash, size, created_at) VALUES (?, 9, ?)`,
		fakeHash("d4"), "2026-01-01T00:00:00.000000000Z")
	require.NoError(t, err)
	_, err = s.db.Exec(
		`INSERT INTO content_versions (
			version_id, node_id, blob_hash, size, recorded_at, node_revision,
			introduced_operation_id, transition_kind
		) VALUES ('44444444-4444-4444-8444-444444444444', ?, ?, 9, ?, 2,
			'dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'content_replace')`,
		live.ID, fakeHash("d4"), "2026-01-01T00:00:00.000000000Z")
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE nodes SET current_version_id =
		'44444444-4444-4444-8444-444444444444', revision = 2 WHERE id = ?`, live.ID)
	require.NoError(t, err)

	// Nothing unreachable yet.
	un, err := s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	assert.Empty(t, un)

	// Trashed-but-not-emptied stays reachable.
	_, _, err = s.Trash(ctx, trashed.ID, -1)
	require.NoError(t, err)
	un, err = s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	assert.Empty(t, un)

	// Hard-deleting a node makes its blob unreachable.
	_, _, err = s.Trash(ctx, gone.ID, -1)
	require.NoError(t, err)
	// Only 'gone' and 'trashed' are in the trash; empty just 'gone' by
	// deleting it directly (EmptyTrash cutoffs are time-based).
	_, err = s.db.Exec(`DELETE FROM nodes WHERE id = ?`, gone.ID)
	require.NoError(t, err)

	un, err = s.UnreachableBlobs(ctx)
	require.NoError(t, err)
	require.Len(t, un, 1)
	assert.Equal(t, fakeHash("c3"), un[0].Hash)
}

func TestDeleteBlobRows(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	packID := pack.NewPackID()

	_, err := s.db.Exec(`INSERT INTO blobs (hash, size, created_at) VALUES (?, 1, ?)`,
		fakeHash("a1"), "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	_, err = s.db.Exec(`
		INSERT INTO blob_packs(store_id,pack_id,entry_count,stored_bytes,created_at)
		VALUES(?,?,1,32,?)`,
		s.primaryStoreID, packID, "2026-01-01T00:00:00Z",
	)
	require.NoError(t, err)
	_, err = s.db.Exec(`
		INSERT INTO blob_pack_entries(
			blob_hash,store_id,pack_id,pack_offset,stored_len,raw_len,flags,crc32c
		) VALUES(?,?,?,?,1,1,0,0)`,
		fakeHash("a1"), s.primaryStoreID, packID, pack.MinEntryOffset,
	)
	require.NoError(t, err)
	_, err = s.db.Exec(`
		INSERT INTO blob_locations(
			blob_hash,store_id,generation,kind,stored_size,pack_eligible
		) VALUES(?,?,?,'packed',1,1)`,
		fakeHash("a1"), s.primaryStoreID, "30000000-0000-4000-8000-000000000001",
	)
	require.NoError(t, err)
	_, err = s.db.Exec(
		`INSERT INTO extracted_text (blob_hash, extractor, extractor_version, status, attempts, text, extracted_at)
		 VALUES (?, 'plain-text', 1, 'ok', 1, 'searchable', ?)`,
		fakeHash("a1"), "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO content_fts(rowid,blob_hash,extractor,text)
		SELECT rowid,blob_hash,extractor,text FROM extracted_text WHERE blob_hash=?`, fakeHash("a1"))
	require.NoError(t, err)

	require.NoError(t, s.DeleteBlobRows(ctx, []string{fakeHash("a1")}))

	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&n))
	assert.Equal(t, 0, n)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM extracted_text`).Scan(&n))
	assert.Equal(t, 0, n)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM content_fts`).Scan(&n))
	assert.Equal(t, 0, n)
	require.NoError(t, s.db.QueryRow(
		`SELECT COUNT(*) FROM blob_pack_entries WHERE blob_hash=?`,
		fakeHash("a1"),
	).Scan(&n))
	assert.Equal(t, 1, n, "dead packed entries remain cataloged until repack")
	var liveEntries int64
	require.NoError(t, s.db.QueryRow(`
		SELECT live_entries FROM blob_packs WHERE store_id=? AND pack_id=?`,
		s.primaryStoreID, packID,
	).Scan(&liveEntries))
	assert.Zero(t, liveEntries)
}

func TestAllBlobs(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.CreateFile(ctx, s.RootID(), "b.txt", fakeHash("b2"), 2, "text/plain")
	require.NoError(t, err)
	_, err = s.CreateFile(ctx, s.RootID(), "a.txt", fakeHash("a1"), 1, "text/plain")
	require.NoError(t, err)

	blobs, err := s.AllBlobs(ctx)
	require.NoError(t, err)
	require.Len(t, blobs, 2)
	assert.Equal(t, fakeHash("a1"), blobs[0].Hash) // hash-ordered
	assert.Equal(t, int64(1), blobs[0].Size)
}
