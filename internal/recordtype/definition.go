// Package recordtype defines and validates the record types shipped by Docbank.
package recordtype

type Definition struct {
	ID          string           `json:"id"`
	Version     int              `json:"version"`
	Label       string           `json:"label"`
	LabelPlural string           `json:"label_plural"`
	Icon        string           `json:"icon"`
	Groups      []Group          `json:"groups"`
	Fields      []Field          `json:"fields"`
	Status      StatusDefinition `json:"status"`
}

type Group struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	EmptyText string `json:"empty_text,omitempty"`
	Retired   bool   `json:"retired,omitempty"`
}
type Field struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Group       string   `json:"group"`
	Pattern     string   `json:"pattern,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
	StartLabel  string   `json:"start_label,omitempty"`
	EndLabel    string   `json:"end_label,omitempty"`
	Retired     bool     `json:"retired,omitempty"`
}
type StatusDefinition struct {
	Values  []Status        `json:"values"`
	Derived []DerivedStatus `json:"derived,omitempty"`
}
type Status struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Initial bool   `json:"initial,omitempty"`
	Tone    string `json:"tone"`
	Retired bool   `json:"retired,omitempty"`
}
type DerivedStatus struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Tone     string   `json:"tone"`
	WhenPast string   `json:"when_past"`
	OnlyFrom []string `json:"only_from"`
	Retired  bool     `json:"retired,omitempty"`
}
type Record struct {
	Title  string
	Note   string
	Status string
	Fields map[string]any
}
