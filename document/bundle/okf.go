package bundle

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"go.kenn.io/docbank/internal/canonical"
)

// OKFExport selects an exact retained rendition and optional disclosure.
type OKFExport struct {
	ProfileFingerprint  string `json:"profile_fingerprint"`
	AttachmentID        string `json:"attachment_id"`
	BuildID             string `json:"build_id"`
	IncludeSourceSHA256 bool   `json:"include_source_sha256,omitzero"`
}

func (o *OKFExport) UnmarshalJSON(raw []byte) error {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if value.Kind() == 'n' {
			return fmt.Errorf("okf.%s must not be null", name)
		}
	}
	for _, name := range []string{"profile_fingerprint", "attachment_id", "build_id"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("okf.%s is required", name)
		}
	}
	type option OKFExport
	var result option
	if err := json.Unmarshal(raw, &result, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if !canonical.IsSHA256Hex(result.ProfileFingerprint) || !canonical.IsSHA256Hex(result.AttachmentID) || !canonical.IsSHA256Hex(result.BuildID) {
		return fmt.Errorf("%w: invalid OKF rendition selector", ErrConflict)
	}
	*o = OKFExport(result)
	return nil
}

func OKFPath(m Member) string {
	return fmt.Sprintf("documents/%d/%s/reference.md", m.NodeID, m.VersionID)
}

// OKFReceipt binds the prepared concept to the source and rendition used.
type OKFReceipt struct {
	Version        int           `json:"version"`
	Source         Member        `json:"source"`
	NodeRevision   int64         `json:"node_revision"`
	Option         OKFExport     `json:"option"`
	ArtifactSHA256 string        `json:"artifact_sha256"`
	ArtifactSize   int64         `json:"artifact_size"`
	Resource       string        `json:"resource"`
	Generated      *OKFGenerated `json:"generated,omitzero"`
	Tags           []string      `json:"tags,omitzero"`
}
type OKFGenerated struct {
	By string `json:"by"`
	At string `json:"at"`
}

func validateOKFRoles(plan Plan, d Document) error {
	if plan.OKF != nil && (plan.Total != 1 || plan.Source.Kind != "explicit" || plan.PhotoRender != nil || plan.VolumeLimits != nil || (plan.DuplicatePolicy != "" && plan.DuplicatePolicy != "preserve") || len(plan.Roles) != 1 || plan.Roles[0] != (RolePolicy{Role: "okf_markdown"})) {
		return ErrConflict
	}
	count := 0
	for _, role := range d.Roles {
		if role.Role != "okf_markdown" {
			continue
		}
		count++
		var receipt OKFReceipt
		if plan.OKF == nil || role.Status != roleAvailable || json.Unmarshal(role.Recipe, &receipt, json.RejectUnknownMembers(true)) != nil || receipt.Version != 1 || receipt.Source != d.Member || receipt.Option != *plan.OKF || receipt.NodeRevision != d.Revision || !canonical.IsSHA256Hex(receipt.ArtifactSHA256) || receipt.ArtifactSize < 0 || receipt.Resource == "" || !canonical.IsSHA256Hex(role.SHA256) || role.Size < 1 || role.Page != nil || role.Path != OKFPath(d.Member) || role.MediaType != "text/markdown; charset=utf-8" {
			return ErrConflict
		}
	}
	if plan.OKF != nil && (count != 1 || len(d.Roles) != 1) {
		return ErrConflict
	}
	return nil
}

// UnmarshalJSON rejects an explicit null option rather than treating it as absent.
func (p *PlanRequest) UnmarshalJSON(raw []byte) error {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if value, ok := fields["okf"]; ok && value.Kind() == 'n' {
		return fmt.Errorf("okf must not be null")
	}
	type planRequest PlanRequest
	return json.Unmarshal(raw, (*planRequest)(p), json.RejectUnknownMembers(true))
}
