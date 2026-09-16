package guidance_test

import (
	"github.com/nimsforest/nimsforesttool/tooltest"
	"testing"
)

func TestSupportingToolContract(t *testing.T) {
	tooltest.Conform(t, tooltest.Options{Package: "./cmd/nimsforestguidance", Args: []string{"serve", "--data", t.TempDir()}, Env: []string{"NATS_URL=nats://127.0.0.1:1"}, DefaultPort: 8128, ExpectDegradedUnconfigured: true})
}
