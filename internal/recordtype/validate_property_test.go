//go:build !darwin || arm64

package recordtype

import (
	"reflect"
	"testing"

	"hegel.dev/go/hegel"
)

func TestContractNormalizationIsIdempotent(t *testing.T) {
	def, _ := Builtin("contract")
	hegel.Test(t, func(ht *hegel.T) {
		input := Record{Title: hegel.Draw(ht, hegel.Text()), Note: hegel.Draw(ht, hegel.Text()), Status: "draft", Fields: map[string]any{"summary": hegel.Draw(ht, hegel.Text())}}
		first, err := NormalizeRecord(def, input)
		if err != nil {
			return
		}
		second, err := NormalizeRecord(def, first)
		if err != nil || !reflect.DeepEqual(first, second) {
			ht.Fatalf("normalization was not idempotent")
		}
	}, hegel.WithTestCases(500), hegel.WithDatabase(t.TempDir()))
}
