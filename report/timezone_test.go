package report

import (
	"errors"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestReportZonesRequireUTCOrIANA(t *testing.T) {
	t.Parallel()
	for _, zone := range []string{"UTC", "America/New_York", "Etc/UTC", "Asia/Kolkata"} {
		t.Run(zone, func(t *testing.T) {
			request := validRequest()
			request.Timezone, request.SourceTimezone = zone, zone
			if _, err := NormalizeRequest(request); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, zone := range []string{"", "Local", "EST", "GMT", "CET", "EST5EDT", "UTC+3", "+03:00", "utc", " America/New_York", "America/New_York ", "posix/America/New_York", "right/America/New_York", "Unknown/Zone"} {
		t.Run("reject/"+zone, func(t *testing.T) {
			request := validRequest()
			request.Timezone = zone
			if _, err := NormalizeRequest(request); err == nil {
				t.Fatal("accepted report zone")
			}
			if _, err := SelectDate("other", nil, nil, request); err == nil || errors.Is(err, ErrUnusableDate) {
				t.Fatalf("direct selection did not reject report zone: %v", err)
			}
			if zone == "" { // An absent source zone deliberately remains optional.
				return
			}
			request = validRequest()
			request.SourceTimezone = zone
			if _, err := NormalizeRequest(request); err == nil {
				t.Fatal("accepted source zone")
			}
			choice := DateChoice{
				Document:    Identity{NodeID: 1, VersionID: "v1", SHA256: strings.Repeat("a", 64)},
				CandidateID: "candidate", EvidenceSHA256: strings.Repeat("b", 64),
				Reason: "Reviewed source date", Action: "interpret", ReviewedDate: "2026-03-08", ReviewedTimezone: zone,
			}
			if err := validateChoiceShape(choice); err == nil {
				t.Fatal("accepted reviewed zone")
			}
			candidate := DateCandidate{ID: "source", Role: "created", SourceClass: "native", Value: "2026-03-08T02:30", Timezone: zone}
			if _, err := SelectDate("other", []DateCandidate{candidate}, nil, Request{Timezone: "UTC"}); !errors.Is(err, ErrUnusableDate) {
				t.Fatalf("accepted candidate zone: %v", err)
			}
		})
	}
}

func TestReportWallClockTransitions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, zone, wall, instant string }{
		{"ordinary", "Asia/Kolkata", "2026-03-08T12:30:00.123456789", "2026-03-08T07:00:00.123456789Z"},
		{"utc", "UTC", "2026-03-08T12:30:00", "2026-03-08T12:30:00Z"},
		{"spring gap", "America/New_York", "2026-03-08T02:30:00", "2026-03-08T07:00:00Z"},
		{"gap start", "America/New_York", "2026-03-08T02:00:00", "2026-03-08T07:00:00Z"},
		{"gap end", "America/New_York", "2026-03-08T03:00:00", "2026-03-08T07:00:00Z"},
		{"fall overlap", "America/New_York", "2026-11-01T01:30:00", "2026-11-01T05:30:00Z"},
		{"overlap fraction", "America/New_York", "2026-11-01T01:30:00.123456789", "2026-11-01T05:30:00.123456789Z"},
		{"Santiago midnight", "America/Santiago", "2026-09-06T00:30:00", "2026-09-06T04:00:00Z"},
		{"Havana midnight", "America/Havana", "2026-03-08T00:30:00", "2026-03-08T05:00:00Z"},
		{"half-hour gap", "Australia/Lord_Howe", "2026-10-04T02:15:00", "2026-10-03T15:30:00Z"},
		{"skipped date", "Pacific/Apia", "2011-12-30T12:00:00", "2011-12-30T10:00:00Z"},
		{"future rules", "America/New_York", "2040-12-31T23:30:00", "2041-01-01T04:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zone, err := loadReportZone(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			wall, err := time.Parse("2006-01-02T15:04:05.999999999", tc.wall)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveReportWallClock(wall, zone)
			if err != nil || got.UTC().Format(time.RFC3339Nano) != tc.instant {
				t.Fatalf("instant=%v err=%v, want %s", got, err, tc.instant)
			}
		})
	}
}

func TestReportSelectionUsesResolvedWallClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, sourceZone, reportZone, value, date string }{
		{"midnight gap", "America/Santiago", "America/Santiago", "2026-09-06T00:30", "2026-09-06"},
		{"space-separated gap", "America/Havana", "America/Havana", "2026-03-08 00:30:00", "2026-03-08"},
		{"overlap earlier", "America/New_York", "America/Belize", "2026-11-01T01:30:00", "2026-10-31"},
		{"gap first real instant", "America/New_York", "America/Phoenix", "2026-03-08T02:30:00", "2026-03-08"},
		{"skipped source date", "Pacific/Apia", "Pacific/Apia", "2011-12-30T12:00:00", "2011-12-31"},
		{"date stays literal", "Pacific/Apia", "Pacific/Apia", "2011-12-30", "2011-12-30"},
		{"instant after date rollback", "UTC", "America/St_Johns", "2010-11-07T03:00:00Z", "2010-11-07"},
		{"resolved clock after date rollback", "UTC", "America/St_Johns", "2010-11-07T03:00:00", "2010-11-07"},
		{"instant before day starts", "UTC", "America/St_Johns", "2010-11-07T02:29:59Z", "2010-11-06"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := DateCandidate{ID: "created", Role: "created", SourceClass: "native", Value: tc.value}
			request := Request{Timezone: tc.reportZone, SourceTimezone: tc.sourceZone}
			got, err := SelectDate("other", []DateCandidate{candidate}, nil, request)
			if err != nil || got.Date != tc.date {
				t.Fatalf("selection=%+v err=%v, want %s", got, err, tc.date)
			}
		})
	}
}

func TestReportReviewedZoneResolvesMidnightGap(t *testing.T) {
	t.Parallel()
	candidate := DateCandidate{
		ID: "created", Role: "created", SourceClass: "native", Value: "2026-09-06T00:30",
		Document:  Identity{NodeID: 1, VersionID: "v1", SHA256: strings.Repeat("a", 64)},
		Rejection: "timezone_omitted", Locator: Locator{EvidenceSHA256: strings.Repeat("b", 64)},
	}
	request := Request{Timezone: "America/Santiago"}
	if _, err := SelectDate("other", []DateCandidate{candidate}, nil, request); !errors.Is(err, ErrUnusableDate) {
		t.Fatalf("timestamp without a source zone became usable: %v", err)
	}
	choice := DateChoice{
		Document: candidate.Document, CandidateID: candidate.ID, EvidenceSHA256: candidate.Locator.EvidenceSHA256,
		Action: "interpret", Reason: "Reviewed source zone", ReviewedDate: "2026-09-06", ReviewedTimezone: "America/Santiago",
	}
	got, err := SelectDate("other", []DateCandidate{candidate}, &choice, request)
	if err != nil || got.Date != choice.ReviewedDate || got.Mode != "override" {
		t.Fatalf("selection=%+v err=%v", got, err)
	}
	choice.ReviewedDate = "2026-09-05"
	if _, err := SelectDate("other", []DateCandidate{candidate}, &choice, request); !errors.Is(err, ErrInvalidChoice) {
		t.Fatalf("accepted the pre-gap date: %v", err)
	}
}
