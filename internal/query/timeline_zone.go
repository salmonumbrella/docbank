package query

import (
	"fmt"
	"strings"
	"time"
)

// LoadZone enforces the timeline zone admission policy.
func LoadZone(name string) (*time.Location, error) {
	if name == "UTC" {
		return time.UTC, nil
	}
	if !strings.Contains(name, "/") || strings.HasPrefix(name, "posix/") || strings.HasPrefix(name, "right/") {
		return nil, fmt.Errorf("invalid timezone %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	return loc, nil
}
