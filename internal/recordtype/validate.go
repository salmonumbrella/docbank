package recordtype

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Reason) }
func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

func NormalizeRecord(def Definition, r Record) (Record, error) {
	if !utf8.ValidString(r.Title) || strings.IndexFunc(r.Title, unicode.IsControl) >= 0 {
		return Record{}, invalid("title", "invalid title")
	}
	r.Title = norm.NFC.String(strings.TrimSpace(r.Title))
	if !utf8.ValidString(r.Title) || len(r.Title) > 1200 || utf8.RuneCountInString(r.Title) < 1 || utf8.RuneCountInString(r.Title) > 300 || strings.IndexFunc(r.Title, unicode.IsControl) >= 0 {
		return Record{}, invalid("title", "invalid title")
	}
	note, err := normalizeLong("note", r.Note)
	if err != nil {
		return Record{}, err
	}
	r.Note = note
	validStatus := false
	for _, s := range def.Status.Values {
		if s.ID == r.Status && !s.Retired {
			validStatus = true
			break
		}
	}
	if !validStatus {
		return Record{}, invalid("status", "unknown status")
	}
	fields := make(map[string]any, len(r.Fields))
	for id, value := range r.Fields {
		var f *Field
		for i := range def.Fields {
			if def.Fields[i].ID == id && !def.Fields[i].Retired {
				f = &def.Fields[i]
				break
			}
		}
		if f == nil {
			return Record{}, invalid(id, "unknown field")
		}
		if value == nil {
			continue
		}
		switch f.Type {
		case "text", "long_text", "label", "date":
			s, ok := value.(string)
			if !ok {
				return Record{}, invalid(id, "invalid value")
			}
			if s == "" {
				continue
			}
			if f.Type == "long_text" || f.Type == "text" {
				s, err = normalizeLong(id, s)
				if err != nil {
					return Record{}, err
				}
			}
			if f.Type == "label" {
				matched, e := regexp.MatchString(f.Pattern, s)
				if e != nil || !matched {
					return Record{}, invalid(id, "invalid label")
				}
			}
			if f.Type == "date" && !ValidDate(s) {
				return Record{}, invalid(id, "invalid date")
			}
			fields[id] = s
		case "date_range":
			m, ok := value.(map[string]any)
			if !ok {
				return Record{}, invalid(id, "invalid date range")
			}
			rangeValue := map[string]any{}
			for k, v := range m {
				if k != "start" && k != "end" {
					return Record{}, invalid(id, "invalid date range")
				}
				if v == nil || v == "" {
					continue
				}
				s, ok := v.(string)
				if !ok || !ValidDate(s) {
					return Record{}, invalid(id, "invalid date")
				}
				rangeValue[k] = s
			}
			start, _ := rangeValue["start"].(string)
			end, _ := rangeValue["end"].(string)
			if start != "" && end != "" && start > end {
				return Record{}, invalid(id, "invalid date order")
			}
			if len(rangeValue) > 0 {
				fields[id] = rangeValue
			}
		default:
			return Record{}, invalid(id, "unknown field type")
		}
	}
	r.Fields = fields
	return r, nil
}

func normalizeLong(field, s string) (string, error) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if !utf8.ValidString(s) || len(s) > 8192 {
		return "", invalid(field, "invalid text")
	}
	for _, c := range s {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return "", invalid(field, "invalid text")
		}
	}
	s = norm.NFC.String(s)
	if len(s) > 8192 {
		return "", invalid(field, "invalid text")
	}
	return s, nil
}

// ValidDate accepts canonical civil dates from year 1 through 9999.
func ValidDate(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Year() >= 1 && t.Format("2006-01-02") == s
}
func EffectiveStatus(def Definition, status string, fields map[string]any, asOf string) string {
	for _, d := range def.Status.Derived {
		if !slices.Contains(d.OnlyFrom, status) || d.Retired {
			continue
		}
		parts := strings.Split(d.WhenPast, ".")
		value := fields[parts[0]]
		if len(parts) == 2 {
			if m, ok := value.(map[string]any); ok {
				value = m[parts[1]]
			} else {
				value = nil
			}
		}
		if date, ok := value.(string); ok && date != "" && date < asOf {
			return d.ID
		}
	}
	return status
}
