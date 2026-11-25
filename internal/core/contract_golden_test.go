package core_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

var updateContract = flag.Bool("update", false, "rewrite testdata/contract.json after an intended contract change")

// TestContractSnapshot locks the agent contract: every tool's input and
// output schema plus the Meta and Error schemas. A rename or type change
// fails here unless testdata/contract.json is regenerated with -update,
// and a regenerated snapshot must carry a new core.Contract: AGENTS.md
// promises the version changes on any breaking rename.
func TestContractSnapshot(t *testing.T) {
	env := coretest.Env(t, nil)
	list := mustInvoke(t, env, "schema", `{}`).Data.([]core.ToolSchema)
	tools := map[string]any{}
	for _, s := range list {
		one := mustInvoke(t, env, "schema", `{"tool":"`+s.Name+`"}`).Data.([]core.ToolSchema)[0]
		tools[s.Name] = map[string]any{"input": one.Input, "output": one.Output, "outputType": one.OutputType, "mutating": one.Mutating}
	}
	meta, err := jsonschema.ForType(reflect.TypeFor[domain.Meta](), nil)
	if err != nil {
		t.Fatal(err)
	}
	errSchema, err := jsonschema.ForType(reflect.TypeFor[core.Error](), nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := map[string]any{"contract": core.Contract, "tools": tools, "meta": meta, "error": errSchema}
	got, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "contract.json")
	if *updateContract {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/core -run Contract -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		var old struct {
			Contract string `json:"contract"`
		}
		_ = json.Unmarshal(want, &old)
		if old.Contract == core.Contract {
			t.Fatalf("the agent contract changed but core.Contract is still %q: bump it (compact.go), note the change in AGENTS.md, then run -update", core.Contract)
		}
		t.Fatalf("contract %s snapshot is stale: run go test ./internal/core -run Contract -update", core.Contract)
	}
}
