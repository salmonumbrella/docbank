package report

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Reports also run in embedded hosts without a zone database.
)

func loadReportZone(name string) (*time.Location, error) {
	if name == "UTC" {
		return time.UTC, nil
	}
	if !strings.Contains(name, "/") || strings.TrimSpace(name) != name ||
		strings.HasPrefix(name, "posix/") || strings.HasPrefix(name, "right/") {
		return nil, fmt.Errorf("invalid timezone %q: expected UTC or an IANA area/location", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	return loc, nil
}

// resolveReportWallClock interprets an already parsed UTC wall-clock value in
// the selected source zone. Repeated clocks use the earlier instant; skipped
// clocks use the first real instant after the gap, including a skipped date.
// This interpretation belongs to report policy, not Kit's calendar-day bounds.
func resolveReportWallClock(wall time.Time, loc *time.Location) (time.Time, error) {
	// IANA offsets are less than 24 hours. Walk every constant-offset interval
	// around the wall clock rather than relying on time.Date's unspecified
	// choice of offset at a transition.
	lo, hi := wall.Add(-48*time.Hour), wall.Add(48*time.Hour)
	var first, gap time.Time
	foundFirst, foundGap := false, false
	for at := lo; at.Before(hi); {
		local := at.In(loc)
		_, offset := local.Zone()
		_, end := local.ZoneBounds()
		if end.IsZero() || end.After(hi) {
			end = hi
		} else if !end.After(at) {
			// POSIX extension rules can return a non-advancing end at year end.
			// Advance by an hour in that case so a later transition is not skipped.
			end = at.Add(time.Hour)
			if end.After(hi) {
				end = hi
			}
		}
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if !candidate.Before(at) && candidate.Before(end) && (!foundFirst || candidate.Before(first)) {
			first, foundFirst = candidate, true
		}
		_, nextOffset := end.In(loc).Zone()
		before := end.Add(time.Duration(offset) * time.Second)
		after := end.Add(time.Duration(nextOffset) * time.Second)
		if !wall.Before(before) && wall.Before(after) && (!foundGap || end.Before(gap)) {
			gap, foundGap = end, true
		}
		at = end
	}
	if foundFirst {
		return first, nil
	}
	if foundGap {
		return gap, nil
	}
	return time.Time{}, fmt.Errorf("cannot resolve wall clock %s in %s", wall.Format(time.RFC3339Nano), loc)
}
