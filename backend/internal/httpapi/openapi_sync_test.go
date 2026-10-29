package httpapi

import (
	"bytes"
	"os"
	"testing"
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
