package httpapi

import (
	"bytes"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The panel's API Docs page serves frontend/public/openapi.yaml (the frontend image
// can't reach docs/), while API changes get documented in docs/openapi.yaml. They
// drifted once - the reseller endpoints existed only in docs/ - so keep them equal.
func TestOpenAPICopiesInSync(t *testing.T) {
	docs, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served, err := os.ReadFile("../../../frontend/public/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, served) {
		t.Fatal("frontend/public/openapi.yaml differs from docs/openapi.yaml - copy docs/openapi.yaml over it")
	}
}

// An unquoted "key: value" inside a description, or a path/schema added twice by two
// branches, still copies cleanly but breaks the API Docs page. yaml.v3 rejects both.
func TestOpenAPIParses(t *testing.T) {
	src, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(src, &spec); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}
	if len(spec.Paths) < 20 {
		t.Fatalf("only %d paths parsed - spec truncated?", len(spec.Paths))
	}
}
