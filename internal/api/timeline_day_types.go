package api

import "time"

type TimelineDaysRequest struct {
	Query     QueryPayload `json:"query"`
	Timezone  *string      `json:"timezone,omitempty"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	DateKinds []string     `json:"date_kinds,omitempty" maxItems:"16"`
}
type TimelineDayRequest struct {
	Query     QueryPayload `json:"query"`
	Timezone  *string      `json:"timezone,omitempty"`
	Date      string       `json:"date"`
	DateKinds []string     `json:"date_kinds,omitempty" maxItems:"16"`
	Cursor    string       `json:"cursor,omitempty" maxLength:"2048"`
	Limit     int          `json:"limit,omitempty"`
}
type TimelineDayCount struct {
	Date     string `json:"date"`
	Count    int64  `json:"count"`
	Floating int64  `json:"floating"`
	DateOnly int64  `json:"date_only"`
}
type TimelineMonthCount struct {
	Month  string `json:"month"`
	Coarse int64  `json:"coarse"`
}
type TimelineState struct {
	PendingVersions int64 `json:"pending_versions"`
}
type TimelineDaysResponse struct {
	Timezone          string               `json:"timezone"`
	TimezoneDefaulted bool                 `json:"timezone_defaulted"`
	From              string               `json:"from"`
	To                string               `json:"to"`
	Days              []TimelineDayCount   `json:"days"`
	Months            []TimelineMonthCount `json:"months"`
	UndatedCount      int64                `json:"undated_count"`
	State             TimelineState        `json:"state"`
}
type TimelineTick struct {
	Minute int    `json:"minute"`
	Label  string `json:"label"`
}
type TimelineBounds struct {
	Start   time.Time      `json:"start"`
	End     time.Time      `json:"end"`
	Minutes int            `json:"minutes"`
	Ticks   []TimelineTick `json:"ticks"`
}
type TimelinePerson struct {
	PersonID      string   `json:"person_id"`
	DisplayName   string   `json:"display_name"`
	Roles         []string `json:"roles"`
	LinkedContact bool     `json:"linked_contact"`
	ArchiveID     string   `json:"archive_id,omitempty"`
	UID           string   `json:"uid,omitempty"`
}
type TimelineCitation struct {
	VaultUID         string `json:"vault_uid"`
	NodeID           int64  `json:"node_id"`
	ContentVersionID string `json:"content_version_id"`
	ContentSHA256    string `json:"content_sha256"`
}
type TimelineItem struct {
	NodeID             int64            `json:"node_id"`
	ContentVersionID   string           `json:"content_version_id"`
	Name               string           `json:"name"`
	Path               string           `json:"path"`
	MediaType          string           `json:"media_type"`
	Size               int64            `json:"size"`
	FamilyParentNodeID *int64           `json:"family_parent_node_id" nullable:"true"`
	When               WhenV1           `json:"when"`
	People             []TimelinePerson `json:"people"`
	UnresolvedCount    int              `json:"unresolved_count"`
	Citation           TimelineCitation `json:"citation"`
}
type TimelineDayResponse struct {
	Date               string         `json:"date"`
	Timezone           string         `json:"timezone"`
	TimezoneDefaulted  bool           `json:"timezone_defaulted"`
	Day                TimelineBounds `json:"day"`
	Items              []TimelineItem `json:"items"`
	DateOnly           []TimelineItem `json:"date_only"`
	DateOnlyTruncated  bool           `json:"date_only_truncated"`
	MonthOnly          []TimelineItem `json:"month_only"`
	MonthOnlyTruncated bool           `json:"month_only_truncated"`
	NextCursor         string         `json:"next_cursor"`
}
