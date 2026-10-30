package nodeinstall

import (
	"bytes"
	"os"
	"testing"
)

func TestEmbeddedScriptMatchesDeploy(t *testing.T) {
	want, err := os.ReadFile("../../../deploy/install-node.sh")
	if err != nil {
		t.Skip("deploy/ not available (building outside the repo checkout)")
	}
	if !bytes.Equal(want, Script) {
		t.Fatal("backend/internal/nodeinstall/install-node.sh is out of sync; run: cp deploy/install-node.sh backend/internal/nodeinstall/install-node.sh")
	}
}
