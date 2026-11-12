package recordtype

import (
	_ "embed"
	"encoding/json/v2"
)

//go:embed contract.json
var contractJSON []byte

// Builtin returns a fresh copy so callers cannot mutate the built-in catalog.
func Builtin(id string) (Definition, bool) {
	if id != "contract" {
		return Definition{}, false
	}
	var d Definition
	if err := json.Unmarshal(contractJSON, &d); err != nil {
		panic(err)
	}
	return d, true
}

func Builtins() []Definition { d, _ := Builtin("contract"); return []Definition{d} }
