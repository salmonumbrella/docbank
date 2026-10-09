package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/store"
)

// PushSourceState is the portable source cursor, which may differ from Node's
// current head after an independent edit. An unknown source has Known=false.
type PushSourceState struct {
	Known bool   `json:"known"`
	Node  *Node  `json:"node,omitzero"`
	Hash  string `json:"hash,omitzero" pattern:"^[0-9a-f]{64}$"`
	Size  int64  `json:"size" minimum:"0"`
}

func registerPushRoutes(api huma.API, d Deps) {
	type input struct {
		Name string `query:"push_name" required:"true" maxLength:"64"`
		Ref  string `query:"source_ref" required:"true" maxLength:"4096"`
	}
	type output struct{ Body PushSourceState }
	huma.Register(api, huma.Operation{
		OperationID: "getPushSource", Method: http.MethodGet, Path: "/api/v1/push/source",
		Summary:     "Read the last accepted bytes for one push source",
		Description: "Uses push name plus relative source path. Unknown identities return known=false; mapped trash is an error. The last accepted source hash is independent of the node's current version.",
	}, func(ctx context.Context, in *input) (*output, error) {
		source := store.PushSource{Name: in.Name, Ref: in.Ref, Duplicates: "link"}
		if err := store.ValidatePushSource(source); err != nil {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", err.Error())
		}
		state, err := d.Store.PushSourceState(ctx, source)
		if errors.Is(err, store.ErrNotFound) {
			return &output{}, nil
		}
		if err != nil {
			return nil, FromStoreError(err)
		}
		node := fromStoreNode(state.Node)
		return &output{Body: PushSourceState{Known: true, Node: &node, Hash: state.Hash, Size: state.Size}}, nil
	})
}
