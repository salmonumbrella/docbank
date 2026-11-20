package store

import (
	"fmt"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document/bundle"
)

func TestOKFPlanAdmission(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := bundle.PlanRequest{OKF: &bundle.OKFExport{ProfileFingerprint: hash, AttachmentID: hash, BuildID: hash}, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}}
	require.NoError(t, validateOKFPlanRequest(&valid))
	tests := []bundle.PlanRequest{
		{Roles: valid.Roles},
		{OKF: valid.OKF, Roles: []bundle.RolePolicy{{Role: "text"}}},
		{OKF: valid.OKF, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}, {Role: "text"}}},
		{OKF: valid.OKF, Roles: valid.Roles, VolumeLimits: &bundle.VolumeLimits{}},
		{OKF: valid.OKF, Roles: valid.Roles, DuplicatePolicy: "collapse_exact_content"},
		{OKF: &bundle.OKFExport{}, Roles: valid.Roles},
	}
	for _, test := range tests {
		require.ErrorIs(t, validateOKFPlanRequest(&test), bundle.ErrConflict)
	}
}

func TestOKFInputExactRendition(t *testing.T) {
	s, versions := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0], BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
	require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("okf-generation"))))
	node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
	require.NoError(t, err)
	m := bundle.Member{NodeID: node.ID, VersionID: versions[0], SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{m}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: attachment.ID, BuildID: build.ID}}
	input, err := s.ExportOKFInput(t.Context(), "owner", request)
	require.NoError(t, err)
	require.Equal(t, m, input.Member)
	require.Equal(t, catalogMarkdownBlobHash, input.ArtifactSHA256)
	request.OKF.BuildID = strings.Repeat("b", 64)
	_, err = s.ExportOKFInput(t.Context(), "owner", request)
	require.Error(t, err)
}

func checkExportOKFPlanSealAndReplay(t *testing.T, open func(*testing.T) *Store) {
	t.Helper()
	s := open(t)
	versions := seedRenditionCatalogVersions(t, s)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0], BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
	require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("okf-generation"))))
	node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
	require.NoError(t, err)
	for _, name := range []string{"éclair", "Zebra"} {
		tag, e := s.CreateTag(t.Context(), name)
		require.NoError(t, e)
		_, e = s.AssignTag(t.Context(), tag.ID, node.ID, node.Revision)
		require.NoError(t, e)
		node, e = s.NodeByID(t.Context(), node.ID)
		require.NoError(t, e)
	}
	m := bundle.Member{NodeID: node.ID, VersionID: versions[0], SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{m}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: attachment.ID, BuildID: build.ID}}
	input, err := s.ExportOKFInput(t.Context(), "owner", request)
	require.NoError(t, err)
	require.Len(t, input.Tags, 2)
	require.Equal(t, "Zebra", input.Tags[0].Name)
	require.Equal(t, "éclair", input.Tags[1].Name)
	a := PreparedOKFExport{Input: input, Receipt: bundle.OKFReceipt{Version: 1, Source: m, NodeRevision: m.Revision, Option: *request.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: "https://example.invalid/content"}, SHA256: fakeHash("b3"), Size: 99, Physical: BlobPhysical{Encoding: looseEncodingRaw, StoredBytes: 99, Created: true}}
	a.Input.Name = "changed"
	_, _, err = s.SealOKFExportPlan(t.Context(), "owner", request, a)
	require.ErrorIs(t, err, bundle.ErrConflict)
	var roots int
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM export_role_roots`).Scan(&roots))
	require.Zero(t, roots)
	a.Input = input
	plan, _, err := s.SealOKFExportPlan(t.Context(), "owner", request, a)
	require.NoError(t, err)
	replay, found, err := s.ExportPlanReplay(t.Context(), "owner", request)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, plan, replay)
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM export_role_roots WHERE plan_id=$1 AND blob_hash=$2`, plan.ID, a.SHA256).Scan(&roots))
	require.Equal(t, 1, roots)

}

func TestOKFSealRejectsChangedAuthority(t *testing.T) {
	checkExportOKFPlanSealAndReplay(t, newTestStore)
}

func TestOKFTagCountAndIdentityBudget(t *testing.T) {
	s, versions := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0], BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
	require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("okf-generation"))))
	node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
	require.NoError(t, err)
	m := bundle.Member{NodeID: node.ID, VersionID: versions[0], SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	option := bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: attachment.ID, BuildID: build.ID}
	for i := 0; i < 257; i++ {
		id := uuid.New().String()
		_, err = s.db.ExecContext(t.Context(), `INSERT INTO tags(id,name) VALUES($1,$2)`, id, fmt.Sprintf("tag-%03d", i))
		require.NoError(t, err)
		_, err = s.db.ExecContext(t.Context(), `INSERT INTO node_tags(node_id,tag_id) VALUES($1,$2)`, node.ID, id)
		require.NoError(t, err)
		if i == 255 {
			input, e := s.exportOKFInput(t.Context(), s.db, m, option)
			require.NoError(t, e)
			require.Len(t, input.Tags, 256)
		}
	}
	_, err = s.exportOKFInput(t.Context(), s.db, m, option)
	require.ErrorIs(t, err, bundle.ErrLimit)
	require.ErrorContains(t, err, "tag IDs and names")
	_, err = s.db.ExecContext(t.Context(), `DELETE FROM node_tags WHERE tag_id=(SELECT id FROM tags WHERE name=$1)`, "tag-256")
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE tags SET name=name || $1`, strings.Repeat("x", 32))
	require.NoError(t, err)
	_, err = s.exportOKFInput(t.Context(), s.db, m, option)
	require.ErrorIs(t, err, bundle.ErrLimit)
	require.ErrorContains(t, err, "16 KiB")
}

func TestOKFAuthorityChangesRefuseSeal(t *testing.T) {
	for _, change := range []string{"tag assign", "tag rename", "tag unassign", "trash", "new version", "other owner", "other version attachment", "changed head"} {
		t.Run(change, func(t *testing.T) {
			s, versions := newRenditionCatalogFixture(t)
			profile := catalogProcessingProfile(t, false)
			build := catalogRenditionBuild(s, profile)
			require.NoError(t, s.StageRenditionBuild(t.Context(), build))
			a := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0], BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
			require.NoError(t, publishRenditionForTest(t, s, a, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("okf-generation"))))
			node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
			require.NoError(t, err)
			tag, err := s.CreateTag(t.Context(), "synthetic")
			require.NoError(t, err)
			if change == "tag rename" || change == "tag unassign" {
				_, err = s.AssignTag(t.Context(), tag.ID, node.ID, node.Revision)
				require.NoError(t, err)
				node, err = s.NodeByID(t.Context(), node.ID)
				require.NoError(t, err)
			}
			m := bundle.Member{NodeID: node.ID, VersionID: versions[0], SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
			source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{m}}, nil)
			require.NoError(t, err)
			request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: a.ID, BuildID: build.ID}}
			input, err := s.ExportOKFInput(t.Context(), "owner", request)
			require.NoError(t, err)
			frozen := PreparedOKFExport{Input: input, Receipt: bundle.OKFReceipt{Version: 1, Source: m, NodeRevision: m.Revision, Option: *request.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: "https://example.invalid"}, SHA256: fakeHash("b3"), Size: 99, Physical: BlobPhysical{Encoding: looseEncodingRaw, StoredBytes: 99, Created: true}}
			owner := "owner"
			switch change {
			case "tag assign":
				_, err = s.AssignTag(t.Context(), tag.ID, node.ID, node.Revision)
			case "tag rename":
				tag, err = s.TagByID(t.Context(), tag.ID)
				require.NoError(t, err)
				_, err = s.RenameTag(t.Context(), tag.ID, tag.Revision, "renamed")
			case "tag unassign":
				_, err = s.UnassignTag(t.Context(), tag.ID, node.ID, node.Revision)
			case "trash":
				_, _, err = s.Trash(t.Context(), node.ID, node.Revision)
			case "new version":
				_, _, err = s.ReplaceContent(t.Context(), node.ID, node.Revision, node.BlobHash, node.Size, node.MimeType)
			case "changed head":
				newBuild := cloneCatalogBuild(build)
				newBuild.ID = testSHA256([]byte("next-okf-build"))
				for i := range newBuild.Artifacts {
					newBuild.Artifacts[i].ID = "artifact_" + testSHA256([]byte(fmt.Sprintf("next-artifact-%d", i)))
				}
				newBuild.Units[0].ID = "rendition_unit_" + testSHA256([]byte("next-unit"))
				newBuild.LexicalSegments[0].ID = "lexical_segment_" + testSHA256([]byte("next-segment"))
				newBuild.LexicalSegments[0].UnitID = newBuild.Units[0].ID
				require.NoError(t, s.StageRenditionBuild(t.Context(), newBuild))
				next := a
				next.BuildID = newBuild.ID
				next.ID = testSHA256([]byte("next-okf-attachment"))
				next.AttachedAt = "2026-08-22T11:00:00.000000000Z"
				err = publishRenditionForTest(t, s, next, "2026-08-22T11:01:00.000000000Z", testSHA256([]byte("next-okf-generation")))
			case "other owner":
				owner = "other"
			case "other version attachment":
				other, e := s.NodeByPath(t.Context(), "/synthetic-source-b.pdf")
				require.NoError(t, e)
				otherMember := bundle.Member{NodeID: other.ID, VersionID: versions[1], SHA256: other.BlobHash, Size: other.Size, Revision: other.Revision}
				otherSource, e := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{otherMember}}, nil)
				require.NoError(t, e)
				otherRequest := request
				otherRequest.SourceID, otherRequest.MemberHash = otherSource.ID, otherSource.MemberHash
				_, e = s.ExportOKFInput(t.Context(), "owner", otherRequest)
				require.Error(t, e)
				return
			}
			require.NoError(t, err)
			_, _, err = s.SealOKFExportPlan(t.Context(), owner, request, frozen)
			require.Error(t, err)
			var count int
			require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM export_role_roots WHERE plan_id=$1`, request.OperationID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestOKFHiddenPhotoRefusesSealWithoutSession(t *testing.T) {
	s, _ := newRenditionCatalogFixture(t)
	profile := catalogProcessingProfile(t, false)
	build := catalogRenditionBuild(s, profile)
	require.NoError(t, s.StageRenditionBuild(t.Context(), build))
	node, err := s.CreateFile(t.Context(), s.RootID(), "synthetic.jpg", catalogSourceHash, int64(len(catalogBlobContents[catalogSourceHash])), "image/jpeg")
	require.NoError(t, err)
	attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: node.CurrentVersionID, BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
	require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("photo-okf-generation"))))
	member := bundle.Member{NodeID: node.ID, VersionID: node.CurrentVersionID, SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
	source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
	require.NoError(t, err)
	request := bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: attachment.ID, BuildID: build.ID}}
	input, err := s.ExportOKFInput(t.Context(), "owner", request)
	require.NoError(t, err)
	prepared := PreparedOKFExport{Input: input, Receipt: bundle.OKFReceipt{Version: 1, Source: member, NodeRevision: member.Revision, Option: *request.OKF, ArtifactSHA256: input.ArtifactSHA256, ArtifactSize: input.ArtifactSize, Resource: "https://example.invalid"}, SHA256: fakeHash("b3"), Size: 99, Physical: BlobPhysical{Encoding: looseEncodingRaw, StoredBytes: 99, Created: true}}
	require.NoError(t, s.SetupPhotoHidden(t.Context(), "synthetic-passcode"))
	asset, err := s.PhotoAssetForNode(t.Context(), node.ID)
	require.NoError(t, err)
	_, err = s.SetPhotoAssetHidden(t.Context(), asset.ID, asset.Revision, true)
	require.NoError(t, err)
	_, _, err = s.SealOKFExportPlan(t.Context(), "owner", request, prepared)
	require.ErrorIs(t, err, ErrHiddenLocked)
}

func TestOKFPopulatedBackendProjection(t *testing.T) {
	project := func(s *Store) map[string]any {
		versions := seedRenditionCatalogVersions(t, s)
		profile := catalogProcessingProfile(t, false)
		build := catalogRenditionBuild(s, profile)
		require.NoError(t, s.StageRenditionBuild(t.Context(), build))
		attachment := RenditionAttachmentRecord{ID: catalogAttachmentFirst, VaultID: s.VaultID(), ContentVersionID: versions[0], BuildID: build.ID, Profile: profile, AttachedAt: "2026-08-22T10:00:00.000000000Z"}
		require.NoError(t, publishRenditionForTest(t, s, attachment, "2026-08-22T10:01:00.000000000Z", testSHA256([]byte("projection-generation"))))
		node, err := s.NodeByPath(t.Context(), "/synthetic-source-a.pdf")
		require.NoError(t, err)
		for _, name := range []string{"éclair", "Zebra"} {
			tag, e := s.CreateTag(t.Context(), name)
			require.NoError(t, e)
			_, e = s.AssignTag(t.Context(), tag.ID, node.ID, node.Revision)
			require.NoError(t, e)
			node, e = s.NodeByID(t.Context(), node.ID)
			require.NoError(t, e)
		}
		member := bundle.Member{NodeID: node.ID, VersionID: versions[0], SHA256: node.BlobHash, Size: node.Size, Revision: node.Revision}
		source, err := s.CreateExportSource(t.Context(), "owner", bundle.SourceRequest{OperationID: uuid.New().String(), Kind: "explicit", Members: []bundle.Member{member}}, nil)
		require.NoError(t, err)
		input, err := s.ExportOKFInput(t.Context(), "owner", bundle.PlanRequest{OperationID: uuid.New().String(), SourceID: source.ID, MemberHash: source.MemberHash, Roles: []bundle.RolePolicy{{Role: "okf_markdown"}}, OKF: &bundle.OKFExport{ProfileFingerprint: profile.Fingerprint, AttachmentID: attachment.ID, BuildID: build.ID}})
		require.NoError(t, err)
		require.Len(t, input.Tags, 2)
		return map[string]any{"name": input.Name, "media_type": input.MediaType, "source_hash": input.Member.SHA256, "source_size": input.Member.Size, "revision": input.Member.Revision, "artifact_hash": input.ArtifactSHA256, "artifact_size": input.ArtifactSize, "build_id": input.Build.ID, "provider_receipt": string(input.Build.ProviderReceipt), "completed_at": input.Build.CompletedAt, "tag_names": []string{input.Tags[0].Name, input.Tags[1].Name}}
	}
	sqlite := project(newTestStore(t))
	postgres := project(newPostgresQueryStore(t))
	require.Equal(t, sqlite, postgres)
}

func TestOKFMixedRoleDiagnostic(t *testing.T) {
	request := bundle.PlanRequest{Roles: []bundle.RolePolicy{{Role: "original"}}, OKF: &bundle.OKFExport{}}
	err := validateOKFPlanRequest(&request)
	require.ErrorIs(t, err, bundle.ErrConflict)
	require.ErrorContains(t, err, "must be supplied together")
	request.Roles = []bundle.RolePolicy{{Role: "okf_markdown"}, {Role: "original"}}
	err = validateOKFPlanRequest(&request)
	require.ErrorIs(t, err, bundle.ErrConflict)
	require.ErrorContains(t, err, "one okf_markdown role")
}
