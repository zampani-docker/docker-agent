package teamloader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/tools"
	skillstool "github.com/docker/docker-agent/pkg/tools/builtin/skills"
)

// TestForkSkillToolsets_Resolved verifies that an inline fork skill that
// declares assistive toolsets has them resolved from the top-level toolsets
// section and exposed through the agent's skills toolset, keyed by skill name.
func TestForkSkillToolsets_Resolved(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")

	data := []byte(`toolsets:
  shell_ts:
    type: shell
agents:
  root:
    model: openai/gpt-4o
    instruction: test
    skills:
      - name: builder
        description: Build things in an isolated context.
        context: fork
        toolsets:
          - shell_ts
        instructions: Do the build.
`)

	team, err := Load(t.Context(), config.NewBytesSource("fork_toolsets.yaml", data), &config.RuntimeConfig{}, withTestProviderRegistry()...)
	require.NoError(t, err)

	root, err := team.Agent("root")
	require.NoError(t, err)

	var skillSet *skillstool.ToolSet
	for _, ts := range root.ToolSets() {
		if s, ok := skillSetFrom(ts); ok {
			skillSet = s
			break
		}
	}
	require.NotNil(t, skillSet, "agent must expose a skills toolset")

	prepared, errResult := skillSet.PrepareForkSubSession(t.Context(), skillstool.RunSkillArgs{Name: "builder", Task: "go"})
	require.Nil(t, errResult)
	require.NotNil(t, prepared)
	assert.Len(t, prepared.ToolSets, 1, "fork skill must carry its resolved assistive toolset")
}

// TestForkSkillToolsets_NonForkSkipped verifies that toolsets declared on a
// non-fork skill are not resolved (they are rejected by config validation, so
// this guards the loader's IsFork gate independently).
func TestForkSkillToolsets_AllowedToolsThreaded(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")

	data := []byte(`agents:
  root:
    model: openai/gpt-4o
    instruction: test
    skills:
      - name: auditor
        description: Read-only audit in an isolated context.
        context: fork
        allowed_tools:
          - read_file
        instructions: Inspect only.
`)

	team, err := Load(t.Context(), config.NewBytesSource("fork_allowed.yaml", data), &config.RuntimeConfig{}, withTestProviderRegistry()...)
	require.NoError(t, err)

	root, err := team.Agent("root")
	require.NoError(t, err)

	var skillSet *skillstool.ToolSet
	for _, ts := range root.ToolSets() {
		if s, ok := skillSetFrom(ts); ok {
			skillSet = s
			break
		}
	}
	require.NotNil(t, skillSet)

	prepared, errResult := skillSet.PrepareForkSubSession(t.Context(), skillstool.RunSkillArgs{Name: "auditor", Task: "go"})
	require.Nil(t, errResult)
	require.NotNil(t, prepared)
	assert.Equal(t, []string{"read_file"}, prepared.AllowedTools)
	assert.Empty(t, prepared.ToolSets)
}

// skillSetFrom unwraps a toolset to a *skillstool.ToolSet if possible.
func skillSetFrom(ts tools.ToolSet) (*skillstool.ToolSet, bool) {
	return tools.As[*skillstool.ToolSet](ts)
}
