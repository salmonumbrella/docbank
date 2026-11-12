//go:build ignore

// Synthetic inputs for document-facts.screenshot.ts:
//
//	pdf <file>                       writes a PDF whose Info names an unresolved
//	                                 author and zone-less (floating) dates
//	assert <vault> <version> <name>  creates an operator person and asserts them
//	                                 as an author of that exact content version
//
// The assertion goes through the store's own AssertDocumentPerson, the path a
// later Tag author control uses; Docbank has no browser or CLI route for it yet.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-pdf/fpdf"
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
		panic("usage: document-facts-fixture pdf <file> | assert <vault> <version> <name>")
	}
	switch os.Args[1] {
	case "pdf":
		writePDF(os.Args[2])
	case "assert":
		if len(os.Args) != 5 {
			panic("usage: document-facts-fixture assert <vault> <version> <name>")
		}
		assertAuthor(os.Args[2], os.Args[3], os.Args[4])
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
