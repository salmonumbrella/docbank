package store

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
)

func validateV29Schema(db *sql.DB, _, _ []string) error {
	layout, err := schemaV29Layout()
	if err != nil {
		return fmt.Errorf("building the v0.15.1 schema layout: %w", err)
	}
	return validateReleasedLayout(db, "v0.15.1", layout)
}

// schemaV29Layout derives the v0.15.1 column map from v0.15.0 plus the
// photo-set tables and receipt column introduced in v0.15.1.
func schemaV29Layout() (string, error) {
	var tables map[string][]string
	if err := json.Unmarshal([]byte(schemaV28Layout), &tables); err != nil {
		return "", fmt.Errorf("decoding v0.15.0 schema layout: %w", err)
	}
	if _, exists := tables["photo_sets"]; exists {
		return "", errors.New("v0.15.0 schema layout unexpectedly contains photo_sets")
	}
	if _, exists := tables["photo_set_members"]; exists {
		return "", errors.New("v0.15.0 schema layout unexpectedly contains photo_set_members")
	}
	receiptColumns, exists := tables["photo_change_receipts"]
	if !exists {
		return "", errors.New("v0.15.0 schema layout is missing photo_change_receipts")
	}
	receiptColumns = append(receiptColumns, "set_id")
	slices.Sort(receiptColumns)
	tables["photo_change_receipts"] = receiptColumns
	tables["photo_sets"] = []string{
		"cover_asset_id", "created_at", "deleted_at", "name", "revision", "set_id", "starred", "updated_at",
	}
	tables["photo_set_members"] = []string{"added_at", "asset_id", "set_id"}

	encoded, err := json.Marshal(tables)
	if err != nil {
		return "", fmt.Errorf("encoding v0.15.1 schema layout: %w", err)
	}
	return string(encoded), nil
}
