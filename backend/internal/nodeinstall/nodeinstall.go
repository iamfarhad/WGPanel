// Package nodeinstall carries the node installer script inside the API binary.
//
// Why embedded rather than fetched: the panel uploads this exact script over SSH, so
// the installer a node gets always matches the panel version that provisioned it - no
// dependence on GitHub being reachable from the node or on "main" having moved on.
// install.sh, by contrast, curls companion files from raw.githubusercontent.com (main
// branch) when run standalone; that is fine for a human bootstrapping the panel, but
// would let a panel and its freshly installed node drift apart.
//
// go:embed cannot reach outside the module, and the API image is built with backend/
// as its context (deploy/ is not in it), so install-node.sh is copied here. A test
// (TestEmbeddedScriptMatchesDeploy) fails when the copy and deploy/install-node.sh
// differ - after editing the deploy script run:
//
//	cp deploy/install-node.sh backend/internal/nodeinstall/install-node.sh
package nodeinstall

import _ "embed"

//go:embed install-node.sh
var Script []byte
