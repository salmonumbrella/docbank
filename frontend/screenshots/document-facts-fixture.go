//go:build ignore

// Synthetic inputs for document-facts.screenshot.ts:
//
//	pdf <file>                       writes a PDF whose Info names an unresolved
//	                                 author and zone-less (floating) dates
//	assert <vault> <version> <name>  creates an operator person and asserts them
//	                                 as an author of that exact content version
//	head <vault> <version>           prints the retained canonical event head of
//	                                 that exact version and the publication fence,
//	                                 without names or addresses
//
// head and assert open the store directly, so the proof runs them only while
// the synthetic daemon is stopped, and the store is closed before it restarts.
// The assertion goes through the store's own AssertDocumentPerson, the path a
// later Tag author control uses; Docbank has no browser or CLI route for it yet.
package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"time"

	"github.com/go-pdf/fpdf"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/home"
	"go.kenn.io/docbank/internal/store"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) < 3 {
		panic("usage: document-facts-fixture pdf <file> | assert <vault> <version> <name> | head <vault> <version>")
	}
	switch os.Args[1] {
	case "pdf":
		writePDF(os.Args[2])
	case "assert":
		if len(os.Args) != 5 {
			panic("usage: document-facts-fixture assert <vault> <version> <name>")
		}
		assertAuthor(os.Args[2], os.Args[3], os.Args[4])
	case "head":
		if len(os.Args) != 4 {
			panic("usage: document-facts-fixture head <vault> <version>")
		}
		// The fixture is a one-shot command with no caller context, so the
		// root context starts here and is passed to every store read.
		printHead(context.Background(), os.Args[2], os.Args[3])
	default:
		panic("unknown mode " + os.Args[1])
	}
}

func writePDF(path string) {
	pdf := fpdf.New("P", "pt", "Letter", "")
	pdf.SetAuthor("L. Beerwijk", false)
	pdf.SetTitle("Board minutes", false)
	// fpdf writes D:YYYYMMDDHHmmSS without a zone, so both dates are floating.
	pdf.SetCreationDate(time.Date(2026, time.March, 10, 23, 30, 0, 0, time.UTC))
	pdf.SetModificationDate(time.Date(2026, time.March, 11, 9, 5, 0, 0, time.UTC))
	pdf.SetFont("Helvetica", "", 12)
	pdf.AddPage()
	pdf.Text(72, 72, "Board minutes, synthetic proof fixture.")
	check(pdf.OutputFileAndClose(path))
}

func assertAuthor(vault, version, name string) {
	ctx := context.Background()
	s, err := store.Open(ctx, home.Layout{Root: vault}.DBPath())
	check(err)
	defer func() { check(s.Close()) }()
	person, err := s.CreatePerson(ctx, name, "operator")
	check(err)
	_, err = s.AssertDocumentPerson(ctx, store.PersonDocumentAssertion{
		ContentVersionID: version, PersonID: person.PersonID,
		Role: "author", Action: "assert", Note: "synthetic proof", Revision: 1,
	})
	check(err)
	fmt.Println(person.PersonID)
}

type headActor struct {
	Role         string `json:"role"`
	Sensitive    bool   `json:"sensitive"`
	EvidenceID   string `json:"evidence_id"`
	EvidenceKind string `json:"evidence_kind"`
}

type headEvent struct {
	EventID      string      `json:"event_id"`
	EvidenceID   string      `json:"evidence_id"`
	EvidenceKind string      `json:"evidence_kind"`
	DateKind     string      `json:"date_kind"`
	UTCKey       string      `json:"utc_key"`
	Sensitive    bool        `json:"sensitive"`
	Safe         bool        `json:"safe"`
	Actors       []headActor `json:"actors"`
}

type headSource struct {
	EvidenceID     string `json:"evidence_id"`
	EvidenceKind   string `json:"evidence_kind"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

type headWitness struct {
	VersionID          string       `json:"version_id"`
	GenerationID       string       `json:"generation_id"`
	Checksum           string       `json:"checksum"`
	InputsSHA256       string       `json:"inputs_sha256"`
	DeriverFingerprint string       `json:"deriver_fingerprint"`
	InputEpoch         int64        `json:"input_epoch"`
	PublishedAt        string       `json:"published_at"`
	StateInputEpoch    int64        `json:"state_input_epoch"`
	DateState          string       `json:"date_state"`
	GlobalPublication  int64        `json:"global_publication_epoch"`
	Sources            []headSource `json:"sources"`
	Events             []headEvent  `json:"events"`
}

// printHead reads the selected canonical event generation for one exact
// version. Safe marks the events the API's safe disclosure keeps, decided by
// the same document.EventHasSensitiveEvidence predicate the route applies.
func printHead(ctx context.Context, vault, version string) {
	s, err := store.Open(ctx, home.Layout{Root: vault}.DBPath())
	check(err)
	defer func() { check(s.Close()) }()
	view, err := s.DocumentEventsForVersion(ctx, version)
	check(err)
	state, err := s.DocumentEventStateRow(ctx, view.Generation.DeriverFingerprint)
	check(err)
	// The store's own freshness verdict for this exact version, the one the
	// dates API serves; when published it must select the same generation.
	facts, err := s.DocumentDateFacts(ctx, version)
	check(err)
	if facts.State == "published" {
		served := facts.View.Generation
		if served.GenerationID != view.Generation.GenerationID ||
			served.Checksum != view.Generation.Checksum ||
			served.InputsSHA256 != view.Generation.InputsSHA256 {
			panic(fmt.Sprintf("fresh generation %s differs from retained %s",
				served.GenerationID, view.Generation.GenerationID))
		}
	}
	out := headWitness{
		VersionID:          view.Version.ID,
		GenerationID:       view.Generation.GenerationID,
		Checksum:           view.Generation.Checksum,
		InputsSHA256:       view.Generation.InputsSHA256,
		DeriverFingerprint: view.Generation.DeriverFingerprint,
		InputEpoch:         view.InputEpoch,
		PublishedAt:        view.PublishedAt,
		StateInputEpoch:    state.InputEpoch,
		DateState:          facts.State,
		GlobalPublication:  state.PublicationEpoch,
		Sources:            []headSource{},
		Events:             []headEvent{},
	}
	for _, source := range view.Events.Sources {
		out.Sources = append(out.Sources, headSource{
			EvidenceID: source.EvidenceID, EvidenceKind: string(source.EvidenceKind),
			EvidenceSHA256: source.EvidenceSHA256,
		})
	}
	for _, event := range view.Events.Events {
		item := headEvent{
			EventID: event.EventID, EvidenceID: event.EvidenceID,
			EvidenceKind: string(event.EvidenceKind), DateKind: string(event.DateKind),
			UTCKey: event.UTCKey, Sensitive: event.Sensitive,
			Safe: !document.EventHasSensitiveEvidence(event), Actors: []headActor{},
		}
		for _, actor := range event.Actors {
			item.Actors = append(item.Actors, headActor{
				Role: string(actor.Role), Sensitive: actor.Sensitive,
				EvidenceID: actor.EvidenceID, EvidenceKind: string(actor.EvidenceKind),
			})
		}
		out.Events = append(out.Events, item)
	}
	encoded, err := json.Marshal(out)
	check(err)
	fmt.Println(string(encoded))
}
