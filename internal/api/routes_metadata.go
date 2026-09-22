package api

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
)

// MetadataValueJSON carries a declared metadata scalar or a string set without
// changing its JSON type at the transport boundary.
type MetadataValueJSON jsontext.Value

func (value MetadataValueJSON) MarshalJSON() ([]byte, error) {
	raw := jsontext.Value(value)
	if !raw.IsValid() {
		return nil, errors.New("metadata value JSON is invalid")
	}
	return raw, nil
}

func (value *MetadataValueJSON) UnmarshalJSON(raw []byte) error {
	if !jsontext.Value(raw).IsValid() {
		return errors.New("metadata value JSON is invalid")
	}
	*value = append((*value)[:0], raw...)
	return nil
}

func (MetadataValueJSON) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		r.Schema(reflect.TypeFor[string](), true, ""),
		r.Schema(reflect.TypeFor[float64](), true, ""),
		r.Schema(reflect.TypeFor[bool](), true, ""),
		{Type: huma.TypeArray, Items: r.Schema(reflect.TypeFor[string](), true, "")},
	}}
}

// MetadataValue is one immutable schema-bound value and its provenance.
type MetadataValue struct {
	DocumentUID      string            `json:"document_uid" format:"uuid"`
	ContentVersionID string            `json:"content_version_id,omitzero" format:"uuid"`
	SchemaUID        string            `json:"schema_uid" format:"uuid"`
	SchemaVersion    int64             `json:"schema_version" minimum:"1"`
	FieldKey         string            `json:"field_key"`
	Value            MetadataValueJSON `json:"value"`
	Lane             string            `json:"lane" enum:"user_override,source_extracted,machine_proposal"`
	Accepted         bool              `json:"accepted"`
	Producer         string            `json:"producer"`
	SourcePointer    string            `json:"source_pointer"`
	CapturedAt       string            `json:"captured_at" format:"date-time"`
	Revision         int64             `json:"revision" minimum:"1"`
}

type MetadataSchemaList struct {
	Schemas []document.MetadataSchema `json:"schemas"`
}

type DocumentMetadata struct {
	DocumentUID string          `json:"document_uid" format:"uuid"`
	Values      []MetadataValue `json:"values"`
}

type metadataSchemaListOutput struct{ Body MetadataSchemaList }
type documentMetadataOutput struct{ Body DocumentMetadata }

func registerMetadataRoutes(api huma.API, d Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "listMetadataSchemas", Method: http.MethodGet, Path: "/api/v1/metadata/schemas",
		Summary: "List immutable metadata schema versions",
	}, func(ctx context.Context, _ *struct{}) (*metadataSchemaListOutput, error) {
		schemas, err := d.Store.MetadataSchemas(ctx)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return &metadataSchemaListOutput{Body: MetadataSchemaList{Schemas: schemas}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getDocumentMetadata", Method: http.MethodGet,
		Path:    "/api/v1/documents/{id}/metadata",
		Summary: "Read retained metadata values by stable document identity",
	}, func(ctx context.Context, in *struct {
		DocumentUID string `path:"id" format:"uuid"`
	}) (*documentMetadataOutput, error) {
		values, err := d.Store.MetadataValues(ctx, in.DocumentUID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		result := DocumentMetadata{DocumentUID: in.DocumentUID, Values: make([]MetadataValue, 0, len(values))}
		for _, value := range values {
			result.Values = append(result.Values, fromStoreMetadataValue(value))
		}
		return &documentMetadataOutput{Body: result}, nil
	})
}

func fromStoreMetadataValue(value store.MetadataValueRecord) MetadataValue {
	return MetadataValue{
		DocumentUID: value.DocumentUID, ContentVersionID: value.ContentVersionID,
		SchemaUID: value.SchemaUID, SchemaVersion: value.SchemaVersion, FieldKey: value.FieldKey,
		Value: MetadataValueJSON(append(jsontext.Value(nil), value.ValueJSON...)), Lane: string(value.Lane),
		Accepted: value.Accepted, Producer: value.Producer, SourcePointer: value.SourcePointer,
		CapturedAt: value.CapturedAt, Revision: value.Revision,
	}
}
