package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"cloud.google.com/go/civil"
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/query"
	"go.kenn.io/docbank/internal/store"
)

func timelineInput(d Deps, raw QueryPayload, zone *string, kinds []string) (store.TimelineScope, error) {
	value, err := parseWorkspaceQuery(raw)
	if err != nil {
		return store.TimelineScope{}, err
	}
	name := d.Cfg.Web.TimelineTimezone
	if name == "" {
		name = "UTC"
	}
	if zone != nil {
		name = *zone
	}
	if _, err := query.LoadZone(name); err != nil {
		return store.TimelineScope{}, NewError(http.StatusBadRequest, "invalid_timezone", err.Error())
	}
	if len(kinds) > 16 {
		return store.TimelineScope{}, NewError(http.StatusBadRequest, "invalid_date_kind", "too many date kinds")
	}
	kinds = slices.Clone(kinds)
	slices.Sort(kinds)
	kinds = slices.Compact(kinds)
	for _, kind := range kinds {
		if !document.ValidDateKind(document.DateKind(kind)) {
			return store.TimelineScope{}, NewError(http.StatusBadRequest, "invalid_date_kind", "unknown date kind")
		}
	}
	return store.TimelineScope{Query: value, Zone: name, Defaulted: zone == nil, DateKinds: kinds}, nil
}
func timelineDate(value string) (civil.Date, error) {
	date, err := civil.ParseDate(value)
	if err != nil {
		return date, NewError(http.StatusBadRequest, "invalid_date", err.Error())
	}
	return date, nil
}
func timelineError(err error) error {
	switch {
	case errors.Is(err, store.ErrTimelineDateNotInZone):
		return NewError(http.StatusBadRequest, "date_not_in_zone", err.Error())
	case errors.Is(err, store.ErrTimelineCursorMismatch):
		return NewError(http.StatusBadRequest, "cursor_mismatch", err.Error())
	default:
		return workspaceQueryError(err)
	}
}

func registerTimelineDayRoutes(api huma.API, d Deps, service *store.QuerySnapshotService) {
	huma.Register(api, huma.Operation{OperationID: "countTimelineDays", Method: http.MethodPost, Path: "/api/v1/timeline/days", Summary: "Count documents per civil day", MaxBodyBytes: maxWorkspaceQueryRequestBytes}, func(ctx context.Context, in *struct{ Body TimelineDaysRequest }) (*struct{ Body TimelineDaysResponse }, error) {
		owner, ok := workspaceSnapshotOwner(ctx)
		if !ok {
			return nil, NewError(http.StatusUnauthorized, "unauthorized", "authenticated snapshot owner is missing")
		}
		if service == nil {
			return nil, NewError(http.StatusServiceUnavailable, "workspace_unavailable", "timeline is unavailable")
		}
		scope, err := timelineInput(d, in.Body.Query, in.Body.Timezone, in.Body.DateKinds)
		if err != nil {
			return nil, err
		}
		from, err := timelineDate(in.Body.From)
		if err != nil {
			return nil, err
		}
		to, err := timelineDate(in.Body.To)
		if err != nil {
			return nil, err
		}
		if from.Compare(to) > 0 {
			return nil, NewError(http.StatusBadRequest, "invalid_date", "from must not follow to")
		}
		if from.AddDays(91).Compare(to) < 0 {
			return nil, NewError(http.StatusBadRequest, "range_too_large", "range exceeds 92 dates")
		}
		loc, _ := query.LoadZone(scope.Zone)
		if _, _, err := query.RangeBounds(from, to, loc); err != nil {
			return nil, NewError(http.StatusBadRequest, "date_not_in_zone", err.Error())
		}
		counts, err := service.TimelineDays(browserPeopleScope(ctx), owner, scope, from, to)
		if err != nil {
			return nil, timelineError(err)
		}
		out := TimelineDaysResponse{Timezone: scope.Zone, TimezoneDefaulted: scope.Defaulted, From: from.String(), To: to.String(), Days: []TimelineDayCount{}, Months: []TimelineMonthCount{}, UndatedCount: counts.Undated, State: TimelineState{PendingVersions: counts.PendingVersions}}
		for _, day := range counts.Days {
			out.Days = append(out.Days, TimelineDayCount{day.Date, day.Count, day.Floating, day.DateOnly})
		}
		for _, month := range counts.Months {
			out.Months = append(out.Months, TimelineMonthCount{month.Month, month.Coarse})
		}
		return &struct{ Body TimelineDaysResponse }{out}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "listTimelineDay", Method: http.MethodPost, Path: "/api/v1/timeline/day", Summary: "List documents on one civil day", MaxBodyBytes: maxWorkspaceQueryRequestBytes}, func(ctx context.Context, in *struct{ Body TimelineDayRequest }) (*struct{ Body TimelineDayResponse }, error) {
		owner, ok := workspaceSnapshotOwner(ctx)
		if !ok {
			return nil, NewError(http.StatusUnauthorized, "unauthorized", "authenticated snapshot owner is missing")
		}
		if service == nil {
			return nil, NewError(http.StatusServiceUnavailable, "workspace_unavailable", "timeline is unavailable")
		}
		scope, err := timelineInput(d, in.Body.Query, in.Body.Timezone, in.Body.DateKinds)
		if err != nil {
			return nil, err
		}
		date, err := timelineDate(in.Body.Date)
		if err != nil {
			return nil, err
		}
		if in.Body.Limit < 0 || in.Body.Limit > 500 {
			return nil, NewError(http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
		}
		hidden := false
		if browserSessionRequest(ctx) {
			state, err := d.Store.PhotoHiddenState(ctx)
			if err != nil {
				return nil, FromStoreError(err)
			}
			hidden = state.ExpiresAt == nil
		}
		page, err := service.TimelineDay(browserPeopleScope(ctx), owner, store.TimelineDayRequest{Scope: scope, Date: date, Cursor: in.Body.Cursor, Limit: in.Body.Limit, HiddenLocked: hidden})
		if err != nil {
			return nil, timelineError(err)
		}
		out := timelinePageAPI(d.Store.VaultID(), date.String(), page)
		return &struct{ Body TimelineDayResponse }{out}, nil
	})
}

func timelinePageAPI(vault, day string, page store.TimelineDayPage) TimelineDayResponse {
	out := TimelineDayResponse{Date: day, Timezone: page.Zone, TimezoneDefaulted: page.Defaulted, Day: TimelineBounds{Start: page.Bounds.Start, End: page.Bounds.End, Minutes: page.Bounds.Minutes, Ticks: []TimelineTick{}}, Items: []TimelineItem{}, DateOnly: []TimelineItem{}, MonthOnly: []TimelineItem{}, DateOnlyTruncated: page.DateOnlyTruncated, MonthOnlyTruncated: page.MonthOnlyTruncated, NextCursor: page.NextCursor}
	for _, tick := range page.Bounds.Ticks {
		out.Day.Ticks = append(out.Day.Ticks, TimelineTick{tick.Minute, tick.Label})
	}
	convert := func(items []store.TimelineItem) []TimelineItem {
		result := make([]TimelineItem, 0, len(items))
		for _, item := range items {
			when := whenFromClaim(item.Event)
			when.PlacedBy.Rule = item.Rule
			if when.State != "coarse" {
				when.Day = day
			}
			when.AxisMinute = item.AxisMinute
			if when.State == "date" && item.EndDate != when.Date {
				when.EndDate = item.EndDate
			}
			people := make([]TimelinePerson, 0, len(item.People))
			for _, person := range item.People {
				people = append(people, TimelinePerson{person.PersonID, person.DisplayName, person.Roles, person.LinkedContact, person.ArchiveID, person.UID})
			}
			result = append(result, TimelineItem{NodeID: item.NodeID, ContentVersionID: item.VersionID, Name: item.Name, Path: item.Path, MediaType: item.MediaType, Size: item.Size, FamilyParentNodeID: item.FamilyParentNodeID, When: when, People: people, UnresolvedCount: item.UnresolvedCount, Citation: TimelineCitation{vault, item.NodeID, item.VersionID, item.BlobHash}})
		}
		return result
	}
	out.Items = convert(page.Items)
	out.DateOnly = convert(page.DateOnly)
	out.MonthOnly = convert(page.MonthOnly)
	return out
}
