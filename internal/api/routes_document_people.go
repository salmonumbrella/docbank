package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/docbank/internal/store"
)

type VersionPersonAssertionChange struct {
	PersonID string `json:"person_id" format:"uuid"`
	Role     string `json:"role" minLength:"1"`
	Action   string `json:"action" enum:"assert,suppress,withdrawn"`
	Note     string `json:"note,omitzero"`
	Revision int64  `json:"revision" minimum:"1"`
}
type PutVersionPersonAssertionsRequest struct {
	Changes []VersionPersonAssertionChange `json:"changes" minItems:"1" maxItems:"32"`
}
type PeopleRebuild struct {
	State string `json:"state" enum:"queued"`
}
type VersionPeopleWrite struct {
	VersionPeople
	Rebuild PeopleRebuild                  `json:"rebuild"`
	Undo    []VersionPersonAssertionChange `json:"undo"`
}

func registerDocumentPeopleWriteRoutes(api huma.API, d Deps, g *gate) {
	huma.Register(api, huma.Operation{OperationID: "putVersionPersonAssertions", Method: http.MethodPut, Path: "/api/v1/versions/{version_id}/people/assertions", MaxBodyBytes: 256 << 10, Summary: "Apply document people decisions"}, func(ctx context.Context, in *struct {
		VersionID string `path:"version_id" format:"uuid"`
		Body      PutVersionPersonAssertionsRequest
	}) (*struct{ Body VersionPeopleWrite }, error) {
		changes := make([]store.DocumentPersonChange, 0, len(in.Body.Changes))
		for _, c := range in.Body.Changes {
			if browserSessionRequest(ctx) && c.Role == "blind_copy" {
				return nil, NewError(http.StatusUnprocessableEntity, "role_forbidden", "Browser sessions may not write blind-copy people")
			}
			changes = append(changes, store.DocumentPersonChange{PersonID: c.PersonID, Role: c.Role, Action: c.Action, Note: c.Note, Revision: c.Revision})
		}
		var result store.DocumentPeopleWrite
		err := g.mutate(ctx, func() error {
			var err error
			result, err = d.Store.AssertDocumentPeople(browserPeopleScope(ctx), in.VersionID, changes)
			if errors.Is(err, store.ErrStaleRevision) {
				return NewError(http.StatusConflict, "stale_revision", err.Error())
			}
			if errors.Is(err, store.ErrNotFound) {
				return factsError(err)
			}
			return FromStoreError(err)
		})
		if err != nil {
			return nil, err
		}
		out := VersionPeopleWrite{VersionPeople: versionPeopleAPI(result.Facts, true, webLaunchURL(d.Cfg.Web.PublicOrigin, d.Cfg.Web.BasePath)), Rebuild: PeopleRebuild{State: "queued"}, Undo: make([]VersionPersonAssertionChange, 0, len(result.Undo))}
		for _, c := range result.Undo {
			out.Undo = append(out.Undo, VersionPersonAssertionChange{PersonID: c.PersonID, Role: c.Role, Action: c.Action, Note: c.Note, Revision: c.Revision})
		}
		return &struct{ Body VersionPeopleWrite }{Body: out}, nil
	})
}
