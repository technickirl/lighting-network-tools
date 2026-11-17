package nodeconfig

import (
	"testing"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

func setDry(t *testing.T, v bool) {
	t.Helper()
	old := luminex.DryRun
	luminex.DryRun = v
	t.Cleanup(func() { luminex.DryRun = old })
}
