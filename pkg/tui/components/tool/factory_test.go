package tool

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/tools/builtin/filesystem"
	shelltool "github.com/docker/docker-agent/pkg/tools/builtin/shell"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/types"
)

func TestRegisterAndResolve(t *testing.T) {
	// Snapshot and restore the package-global custom registry so the test does
	// not leak registrations into other tests.
	customMu.Lock()
	saved := custom
	custom = map[string]Builder{}
	customMu.Unlock()
	t.Cleanup(func() {
		customMu.Lock()
		custom = saved
		customMu.Unlock()
	})

	// Unknown, unregistered key resolves to nothing.
	_, ok := resolve("add")
	assert.False(t, ok)

	// A new tool name resolves to its registered renderer.
	customCalled := false
	Register("add", func(*types.Message, service.SessionStateReader) layout.Model {
		customCalled = true
		return nil
	})
	b, ok := resolve("add")
	assert.True(t, ok)
	b(nil, nil)
	assert.True(t, customCalled)

	// A custom renderer takes precedence over a built-in one for the same key.
	overrodeBuiltin := false
	Register(shelltool.ToolNameShell, func(*types.Message, service.SessionStateReader) layout.Model {
		overrodeBuiltin = true
		return nil
	})
	b, ok = resolve(shelltool.ToolNameShell)
	assert.True(t, ok)
	b(nil, nil)
	assert.True(t, overrodeBuiltin)

	// A built-in with no custom override still resolves to its built-in renderer.
	_, ok = resolve(filesystem.ToolNameReadFile)
	assert.True(t, ok)
}
