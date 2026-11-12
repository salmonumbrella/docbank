package processing

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
)

func TestEmailDateZones(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ zone, kind string }{
		{"UT", "rfc5322"}, {"gMt", "rfc5322"}, {"est", "rfc5322"}, {"EDT", "rfc5322"}, {"CST", "rfc5322"}, {"CDT", "rfc5322"}, {"MST", "rfc5322"}, {"MDT", "rfc5322"}, {"PST", "rfc5322"}, {"PDT", "rfc5322"}, {"UTC", "utc"}, {"utc", "utc"}, {"A", "omitted"}, {"M", "omitted"}, {"z", "omitted"}, {"J", "named"}, {"CET", "named"}, {"+1800", "offset"}, {"+1801", "invalid"}, {"+0000", "utc"}, {"-0000", "omitted"}, {"Z", "omitted"}, {"+1930", "invalid"}, {"+1260", "invalid"},
	} {
		event, ok := rawEmailDateEvent("10 Mar 2026 12:30 " + tc.zone)
		if !ok || string(event.TimezoneKind) != tc.kind {
			t.Errorf("%s: %+v parsed %v", tc.zone, event, ok)
		}
		if tc.kind == "invalid" && (event.ZoneText != tc.zone || event.OffsetSeconds != nil || event.UTCKey != "") {
			t.Errorf("%s: %+v", tc.zone, event)
		}
	}
	for _, tc := range []struct {
		zone    string
		seconds int
	}{
		{"UT", 0}, {"gMt", 0}, {"est", -5 * 3600}, {"EDT", -4 * 3600}, {"CST", -6 * 3600}, {"CDT", -5 * 3600}, {"MST", -7 * 3600}, {"MDT", -6 * 3600}, {"PST", -8 * 3600}, {"PDT", -7 * 3600}, {"+1800", 18 * 3600},
	} {
		event, ok := rawEmailDateEvent("10 Mar 2026 12:30 " + tc.zone)
		if !ok || event.OffsetSeconds == nil || *event.OffsetSeconds != tc.seconds || event.UTCKey == "" {
			t.Errorf("%s: %+v parsed %v", tc.zone, event, ok)
		}
	}
}

func TestInvalidEmailOffsetRetainedThroughMetadata(t *testing.T) {
	t.Parallel()
	raw := "10 Mar 2026 12:30 +1930"
	record := document.SourceMetadataV1{}
	collector := metadataCollector{record: &record, seen: make(map[string]bool)}
	collector.emailTimestamp(document.EmailDateV1{State: document.EmailDateInvalid}, raw)
	if len(record.Fields) != 1 || record.Fields[0].Key != "email.sent.raw" {
		t.Fatalf("fields = %+v", record.Fields)
	}
}

// TestEmailDateExtractionToEvent exercises the shared decoder and metadata
// extractor before event derivation, including inputs the decoder cannot normalize.
func TestEmailDateExtractionToEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, headers, raw, value string
		precision                 document.EventPrecision
		zone                      document.EventTimezoneKind
		zoneText, utc             string
		hasOffset                 bool
		offset                    int
		wantSent                  bool
	}{
		{name: "weekday_mismatch", headers: "Date: Tue, 09 Sep 2026 12:34:56 +0000\r\n"},
		{name: "unknown_weekday", headers: "Date: Fry, 2 Jan 2024 03:04 +0000\r\n"},
		{name: "invalid_calendar", headers: "Date: 30 Feb 2024 03:04 +0000\r\n"},
		{name: "ambiguous_duplicate", headers: "Date: Tue, 2 Jan 2024 03:04 +0000\r\nDate: Tue, 2 Jan 2024 04:04 +0000\r\n"},
		{name: "valid_weekday_compact_utc", headers: "Date: Tue, 2 Jan 2024 03:04 +0000\r\n", raw: "Tue, 2 Jan 2024 03:04 +0000", value: "2024-01-02T03:04", precision: "minute", zone: "utc", zoneText: "+0000", utc: "2024-01-02T03:04:00.000000000Z", wantSent: true},
		{name: "colon_positive_zero", headers: "Date: Tue, 2 Jan 2024 03:04 +00:00\r\n", raw: "Tue, 2 Jan 2024 03:04 +00:00", value: "2024-01-02T03:04", precision: "minute", zone: "utc", zoneText: "+00:00", utc: "2024-01-02T03:04:00.000000000Z", wantSent: true},
		{name: "colon_negative_zero", headers: "Date: Tue, 2 Jan 2024 03:04 -00:00\r\n", raw: "Tue, 2 Jan 2024 03:04 -00:00", value: "2024-01-02T03:04", precision: "minute", zone: "omitted", zoneText: "-00:00", wantSent: true},
		{name: "compact_negative_zero", headers: "Date: Tue, 2 Jan 2024 03:04 -0000\r\n", raw: "Tue, 2 Jan 2024 03:04 -0000", value: "2024-01-02T03:04", precision: "minute", zone: "omitted", zoneText: "-0000", wantSent: true},
		{name: "colon_offset", headers: "Date: Tue, 2 Jan 2024 03:04 +05:30\r\n", raw: "Tue, 2 Jan 2024 03:04 +05:30", value: "2024-01-02T03:04", precision: "minute", zone: "offset", zoneText: "+05:30", utc: "2024-01-01T21:34:00.000000000Z", hasOffset: true, offset: 19800, wantSent: true},
		{name: "compact_offset", headers: "Date: Tue, 2 Jan 2024 03:04 -0700\r\n", raw: "Tue, 2 Jan 2024 03:04 -0700", value: "2024-01-02T03:04", precision: "minute", zone: "offset", zoneText: "-0700", utc: "2024-01-02T10:04:00.000000000Z", hasOffset: true, offset: -25200, wantSent: true},
		{name: "colon_limit", headers: "Date: Tue, 2 Jan 2024 03:04 +18:00\r\n", raw: "Tue, 2 Jan 2024 03:04 +18:00", value: "2024-01-02T03:04", precision: "minute", zone: "offset", zoneText: "+18:00", utc: "2024-01-01T09:04:00.000000000Z", hasOffset: true, offset: 64800, wantSent: true},
		{name: "compact_limit", headers: "Date: Tue, 2 Jan 2024 03:04 +1800\r\n", raw: "Tue, 2 Jan 2024 03:04 +1800", value: "2024-01-02T03:04", precision: "minute", zone: "offset", zoneText: "+1800", utc: "2024-01-01T09:04:00.000000000Z", hasOffset: true, offset: 64800, wantSent: true},
		{name: "short_numeric_one_digit", headers: "Date: Tue, 2 Jan 2024 03:04 +4\r\n", raw: "Tue, 2 Jan 2024 03:04 +4", value: "2024-01-02T03:04", precision: "minute", zone: "invalid", zoneText: "+4", wantSent: true},
		{name: "short_numeric_two_digits", headers: "Date: Tue, 2 Jan 2024 03:04 +04\r\n", raw: "Tue, 2 Jan 2024 03:04 +04", value: "2024-01-02T03:04", precision: "minute", zone: "invalid", zoneText: "+04", wantSent: true},
		{name: "colon_out_of_range", headers: "Date: Tue, 2 Jan 2024 03:04 +25:00\r\n", raw: "Tue, 2 Jan 2024 03:04 +25:00", value: "2024-01-02T03:04", precision: "minute", zone: "invalid", zoneText: "+25:00", wantSent: true},
		{name: "compact_out_of_range", headers: "Date: Tue, 2 Jan 2024 03:04 +1930\r\n", raw: "Tue, 2 Jan 2024 03:04 +1930", value: "2024-01-02T03:04", precision: "minute", zone: "invalid", zoneText: "+1930", wantSent: true},
		{name: "rfc_fixed_zone", headers: "Date: Tue, 2 Jan 2024 03:04 EST\r\n", raw: "Tue, 2 Jan 2024 03:04 EST", value: "2024-01-02T03:04", precision: "minute", zone: "rfc5322", zoneText: "EST", utc: "2024-01-02T08:04:00.000000000Z", hasOffset: true, offset: -18000, wantSent: true},
		{name: "unknown_zone", headers: "Date: Tue, 2 Jan 2024 03:04 CET\r\n", raw: "Tue, 2 Jan 2024 03:04 CET", value: "2024-01-02T03:04", precision: "minute", zone: "named", zoneText: "CET", wantSent: true},
		{name: "military_zone", headers: "Date: Tue, 2 Jan 2024 03:04 Z\r\n", raw: "Tue, 2 Jan 2024 03:04 Z", value: "2024-01-02T03:04", precision: "minute", zone: "omitted", zoneText: "Z", wantSent: true},
		{name: "no_zone", headers: "Date: Tue, 2 Jan 2024 03:04\r\n", raw: "Tue, 2 Jan 2024 03:04", value: "2024-01-02T03:04", precision: "minute", zone: "omitted", wantSent: true},
		{name: "fraction_two_digit_year_comment", headers: "Date: Tue, 2 Jan 24 03:04:05.125 +0000 (synthetic)\r\n", raw: "Tue, 2 Jan 24 03:04:05.125 +0000 (synthetic)", value: "2024-01-02T03:04:05.125", precision: "fraction", zone: "utc", zoneText: "+0000", utc: "2024-01-02T03:04:05.125000000Z", wantSent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("From: Ada <ada@example.test>\r\n" + tc.headers + "\r\nbody")
			metadata, err := ExtractSourceMetadata(t.Context(), sourceMetadataTestSpool(t), payload)
			require.NoError(t, err)
			input := canonicalDocumentEventInput(t, metadata.Fields)
			record, _, err := DeriveDocumentEvents(input)
			require.NoError(t, err)
			var sent []document.DocumentEventV1
			for _, event := range record.Events {
				if event.DateKind == "sent" {
					sent = append(sent, event)
				}
			}
			if !tc.wantSent {
				require.Empty(t, sent, "rejected or ambiguous Date must not yield a sent event")
				return
			}
			require.Len(t, sent, 1, "extracted Date must yield one sent event")
			event := sent[0]
			require.Equal(t, tc.raw, event.RawValue)
			require.Equal(t, tc.value, event.DateValue)
			require.Equal(t, tc.precision, event.Precision)
			if tc.precision == "fraction" {
				require.Equal(t, 3, event.FractionDigits)
			}
			require.Equal(t, tc.zone, event.TimezoneKind)
			require.Equal(t, tc.zoneText, event.ZoneText)
			require.Equal(t, tc.utc, event.UTCKey)
			if tc.hasOffset {
				require.NotNil(t, event.OffsetSeconds)
				require.Equal(t, tc.offset, *event.OffsetSeconds)
			} else {
				require.Nil(t, event.OffsetSeconds)
			}
		})
	}
}
