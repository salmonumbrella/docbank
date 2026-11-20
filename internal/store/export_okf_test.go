package store

import (
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
	_, err = s.SealOKFExportPlan(t.Context(), "owner", request, a)
	require.ErrorIs(t, err, bundle.ErrConflict)
	var roots int
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM export_role_roots`).Scan(&roots))
	require.Zero(t, roots)
	a.Input = input
	plan, err := s.SealOKFExportPlan(t.Context(), "owner", request, a)
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
