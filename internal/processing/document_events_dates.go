package processing

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/docbank/document"
)

var (
	calendarComponentDate = regexp.MustCompile(`^calendar\.event\.e[0-9]{6}\.(start|end)$`)
	metadataOffset        = regexp.MustCompile(`^[+-](0[0-9]|1[0-4]):[0-5][0-9]$`)
	emailDate             = regexp.MustCompile(`^(?:[A-Za-z]{3},\s*)?([0-9]{1,2})\s+([A-Za-z]{3})\s+([0-9]{2}|[0-9]{4})\s+([0-9]{2}):([0-9]{2})(?::([0-9]{2})(\.[0-9]{1,9})?)?\s+([+-][0-9]{4}|[A-Za-z]{1,10})(?:\s+\([^\r\n]*\))?$`)
)

func metadataTimestampEvent(stamp document.SourceMetadataTimestampV1) (document.DocumentEventV1, error) {
	precision := document.EventPrecision(stamp.Precision)
	dateValue := stamp.Normalized
	zone := document.EventTimezoneKind(stamp.Timezone)
	var zoneText string
	var offsetSeconds *int
	switch stamp.Timezone {
	case document.SourceMetadataTimezoneOmitted:
	case document.SourceMetadataTimezoneUTC:
		if !strings.HasSuffix(dateValue, "Z") {
			return document.DocumentEventV1{}, errors.New("UTC timestamp has no Z suffix")
		}
		dateValue = strings.TrimSuffix(dateValue, "Z")
		zoneText = "Z"
	case document.SourceMetadataTimezoneOffset:
		if !metadataOffset.MatchString(stamp.Offset) || !strings.HasSuffix(dateValue, stamp.Offset) {
			return document.DocumentEventV1{}, errors.New("timestamp has an invalid offset")
		}
		dateValue = strings.TrimSuffix(dateValue, stamp.Offset)
		zoneText = stamp.Offset
		seconds, err := offsetTokenSeconds(stamp.Offset)
		if err != nil {
			return document.DocumentEventV1{}, err
		}
		offsetSeconds = &seconds
	default:
		return document.DocumentEventV1{}, errors.New("timestamp has an invalid timezone kind")
	}
	if dateValue == "" {
		return document.DocumentEventV1{}, errors.New("timestamp normalization is empty")
	}
	event := document.DocumentEventV1{
		Actors: []document.DocumentEventActorV1{}, DateValue: dateValue,
		Precision: precision, TimezoneKind: zone, ZoneText: zoneText,
		OffsetSeconds: offsetSeconds,
	}
	axis, err := document.EventAxisKey(dateValue, precision, zone, offsetSeconds)
	if err != nil {
		return document.DocumentEventV1{}, err
	}
	utc, _, err := document.EventUTCKey(dateValue, precision, zone, offsetSeconds)
	if err != nil {
		return document.DocumentEventV1{}, err
	}
	event.AxisKey = axis
	event.UTCKey = utc
	if precision == "fraction" {
		event.FractionDigits = len(dateValue) - strings.LastIndexByte(dateValue, '.') - 1
	}
	return event, nil
}

func rawEmailDateEvent(raw string) (document.DocumentEventV1, bool) {
	match := emailDate.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return document.DocumentEventV1{}, false
	}
	monthTime, err := time.Parse("Jan", canonicalRFCMonth(match[2]))
	if err != nil {
		return document.DocumentEventV1{}, false
	}
	day, _ := strconv.Atoi(match[1])
	year, _ := strconv.Atoi(match[3])
	if len(match[3]) == 2 {
		if year <= 68 {
			year += 2000
		} else {
			year += 1900
		}
	}
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second := 0
	if match[6] != "" {
		second, _ = strconv.Atoi(match[6])
	}
	calendar := time.Date(year, monthTime.Month(), day, hour, minute, second, 0, time.UTC)
	if year < 1 || year > 9999 || calendar.Year() != year || calendar.Month() != monthTime.Month() ||
		calendar.Day() != day || calendar.Hour() != hour || calendar.Minute() != minute || calendar.Second() != second {
		return document.DocumentEventV1{}, false
	}
	base := fmt.Sprintf("%04d-%02d-%02dT%02d:%02d", year, int(monthTime.Month()), day, hour, minute)
	precision := document.EventPrecision("minute")
	if match[6] != "" {
		base += fmt.Sprintf(":%02d", second)
		precision = "second"
	}
	if match[7] != "" {
		base += match[7]
		precision = "fraction"
	}
	zoneToken := match[8]
	zone := document.EventTimezoneKind("omitted")
	var zoneText string
	var offset *int
	if strings.HasPrefix(zoneToken, "+") || strings.HasPrefix(zoneToken, "-") {
		if zoneToken == "-0000" {
			zoneText = zoneToken
		} else {
			colon := zoneToken[:3] + ":" + zoneToken[3:]
			seconds, offsetErr := offsetTokenSeconds(colon)
			if offsetErr != nil {
				return document.DocumentEventV1{}, false
			}
			zone = "offset"
			zoneText = zoneToken
			offset = &seconds
		}
	} else {
		zone = "named"
		zoneText = zoneToken
	}
	axis, err := document.EventAxisKey(base, precision, zone, offset)
	if err != nil {
		return document.DocumentEventV1{}, false
	}
	utc, _, err := document.EventUTCKey(base, precision, zone, offset)
	if err != nil {
		return document.DocumentEventV1{}, false
	}
	event := document.DocumentEventV1{
		Actors: []document.DocumentEventActorV1{}, AxisKey: axis, UTCKey: utc,
		DateValue: base, Precision: precision, TimezoneKind: zone,
		ZoneText: zoneText, OffsetSeconds: offset,
	}
	if precision == "fraction" {
		event.FractionDigits = len(match[7]) - 1
	}
	return event, true
}

func canonicalRFCMonth(value string) string {
	lower := strings.ToLower(value)
	if len(lower) != 3 {
		return value
	}
	return strings.ToUpper(lower[:1]) + lower[1:]
}

func rfc3339Event(value string) (document.DocumentEventV1, error) {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return document.DocumentEventV1{}, fmt.Errorf("parsing RFC3339 event time: %w", err)
	}
	precision := document.SourceMetadataPrecisionSecond
	if strings.IndexByte(value, '.') >= 0 {
		precision = document.SourceMetadataPrecisionFraction
	}
	stamp := document.SourceMetadataTimestampV1{
		Normalized: value, Raw: value, Precision: precision,
	}
	if strings.HasSuffix(value, "Z") {
		stamp.Timezone = document.SourceMetadataTimezoneUTC
	} else {
		stamp.Timezone = document.SourceMetadataTimezoneOffset
		stamp.Offset = value[len(value)-6:]
	}
	return metadataTimestampEvent(stamp)
}

func offsetTokenSeconds(token string) (int, error) {
	if !metadataOffset.MatchString(token) {
		return 0, errors.New("timestamp has an invalid offset")
	}
	hours, _ := strconv.Atoi(token[1:3])
	minutes, _ := strconv.Atoi(token[4:6])
	seconds := hours*3600 + minutes*60
	if token[0] == '-' {
		seconds = -seconds
	}
	return seconds, nil
}
