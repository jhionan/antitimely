package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/macos"
)

// TestDaemonPipelineUsesRealHerdrSessionPath pins the single line that makes
// herdr space attribution live in production. NewPipeline defaults p.herdr to
// a resolver with an empty path — inert, so unit tests need no herdr state —
// which means a refactor that dropped the production wiring would disable
// space attribution entirely with every other test still green, and every
// space-bound project would silently start billing by cwd instead.
func TestDaemonPipelineUsesRealHerdrSessionPath(t *testing.T) {
	p := newDaemonPipeline(nil, &macos.FakeBridge{}, NewCache(), PipelineConfig{})
	if p.herdr == nil {
		t.Fatal("the daemon's pipeline has no herdr resolver: space attribution is off")
	}

	want := herdr.DefaultSessionPath()
	if want == "" {
		t.Fatal("herdr.DefaultSessionPath() is empty; the daemon would resolve no spaces")
	}
	if got := p.herdr.Path(); got != want {
		t.Fatalf("pipeline resolver path = %q, want the real session path %q", got, want)
	}
	if filepath.Base(want) != "session.json" || !strings.Contains(want, "herdr") {
		t.Fatalf("DefaultSessionPath() = %q, want herdr's session.json", want)
	}

	// And the inert default really is inert, which is what makes the check
	// above meaningful rather than tautological.
	if def := NewPipeline(nil, &macos.FakeBridge{}, NewCache(), PipelineConfig{}); def.herdr.Path() != "" {
		t.Fatalf("NewPipeline's default resolver path = %q, want empty", def.herdr.Path())
	}
}
