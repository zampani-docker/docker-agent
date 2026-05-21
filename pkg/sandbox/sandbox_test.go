package sandbox_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/sandbox"
)

func TestCheckAvailable(t *testing.T) {
	tests := []struct {
		name      string
		script    string // empty means no fake binary (docker not found)
		wantErr   string
		wantNoErr bool
	}{
		{
			name:    "no docker installed",
			wantErr: "--sandbox requires Docker Desktop",
		},
		{
			name:    "docker without sandbox support",
			script:  "#!/bin/sh\nexit 1\n",
			wantErr: "--sandbox requires Docker Desktop with sandbox support",
		},
		{
			name:      "docker with sandbox support",
			script:    "#!/bin/sh\nexit 0\n",
			wantNoErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			if tt.script != "" {
				require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(tt.script), 0o755))
			}
			t.Setenv("PATH", fakeDir)

			backend := sandbox.NewBackend(false)
			err := backend.CheckAvailable(t.Context())
			if tt.wantNoErr {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestForWorkspace(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wd       string
		wantName string
	}{
		{
			name:     "matching workspace",
			json:     `{"vms":[{"name":"my-sandbox","workspaces":["/my/project"]}]}`,
			wd:       "/my/project",
			wantName: "my-sandbox",
		},
		{
			name: "no match",
			json: `{"vms":[{"name":"other","workspaces":["/other/project"]}]}`,
			wd:   "/my/project",
		},
		{
			name: "empty list",
			json: `{"vms":[]}`,
			wd:   "/my/project",
		},
		{
			name:     "multiple sandboxes",
			json:     `{"vms":[{"name":"a","workspaces":["/a"]},{"name":"b","workspaces":["/b"]}]}`,
			wd:       "/b",
			wantName: "b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDir := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", tt.json)
			require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(script), 0o755))
			t.Setenv("PATH", fakeDir)

			backend := sandbox.NewBackend(false)
			got := backend.ForWorkspace(t.Context(), tt.wd)
			if tt.wantName == "" {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantName, got.Name)
			}
		})
	}
}

func TestExisting_HasWorkspace(t *testing.T) {
	t.Parallel()

	s := &sandbox.Existing{
		Name:       "test",
		Workspaces: []string{"/workspace", "/extra:ro"},
	}

	assert.True(t, s.HasWorkspace("/workspace"))
	assert.True(t, s.HasWorkspace("/extra"), "should match ignoring :ro suffix")
	assert.False(t, s.HasWorkspace("/other"))
}

func TestNewBackend_PrefersSbx(t *testing.T) {
	fakeDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "sbx"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", fakeDir)

	// When sbx is available and preferred, CheckAvailable uses sbx.
	backend := sandbox.NewBackend(true)
	err := backend.CheckAvailable(t.Context())
	require.NoError(t, err)
}

func TestNewBackend_FallsBackToDocker(t *testing.T) {
	fakeDir := t.TempDir()
	// Only docker is available, no sbx.
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", fakeDir)

	backend := sandbox.NewBackend(true)
	err := backend.CheckAvailable(t.Context())
	require.NoError(t, err)
}

func TestForWorkspace_SbxBackend(t *testing.T) {
	fakeDir := t.TempDir()
	jsonData := `{"sandboxes":[{"name":"my-sbx","workspaces":["/my/project"]}]}`
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", jsonData)
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "sbx"), []byte(script), 0o755))
	t.Setenv("PATH", fakeDir)

	backend := sandbox.NewBackend(true)
	got := backend.ForWorkspace(t.Context(), "/my/project")
	require.NotNil(t, got)
	assert.Equal(t, "my-sbx", got.Name)
}

func TestExtraWorkspace(t *testing.T) {
	t.Run("empty ref", func(t *testing.T) {
		assert.Empty(t, sandbox.ExtraWorkspace("/workspace", ""))
	})

	t.Run("built-in name", func(t *testing.T) {
		assert.Empty(t, sandbox.ExtraWorkspace("/workspace", "default"))
	})

	t.Run("OCI reference", func(t *testing.T) {
		assert.Empty(t, sandbox.ExtraWorkspace("/workspace", "docker.io/myorg/agent:latest"))
	})

	t.Run("yaml outside workspace", func(t *testing.T) {
		agentDir := t.TempDir()
		agent := filepath.Join(agentDir, "agent.yaml")
		require.NoError(t, os.WriteFile(agent, []byte("x"), 0o600))

		got := sandbox.ExtraWorkspace(t.TempDir(), agent)
		assert.Equal(t, agentDir, got)
	})

	t.Run("yaml inside workspace", func(t *testing.T) {
		wd := t.TempDir()
		sub := filepath.Join(wd, "sub")
		require.NoError(t, os.Mkdir(sub, 0o755))
		agent := filepath.Join(sub, "agent.yaml")
		require.NoError(t, os.WriteFile(agent, []byte("x"), 0o600))

		assert.Empty(t, sandbox.ExtraWorkspace(wd, agent))
	})

	t.Run("alias points to file outside workspace", func(t *testing.T) {
		// Regression: ExtraWorkspace used to call filepath.Abs("gopher")
		// directly and miss the alias hop, returning "". The sandbox
		// would then launch without the alias's target YAML mounted
		// and the in-sandbox docker-agent could not read it.
		agentDir := t.TempDir()
		agent := filepath.Join(agentDir, "gopher.yaml")
		require.NoError(t, os.WriteFile(agent, []byte("x"), 0o600))

		writeAlias(t, "gopher", agent)

		got := sandbox.ExtraWorkspace(t.TempDir(), "gopher")
		assert.Equal(t, agentDir, got)
	})

	t.Run("alias points to OCI reference", func(t *testing.T) {
		// OCI-backed aliases have nothing on the host filesystem to
		// mount; ExtraWorkspace returns "".
		writeAlias(t, "remote", "docker.io/myorg/agent:latest")

		assert.Empty(t, sandbox.ExtraWorkspace(t.TempDir(), "remote"))
	})
}

// writeAlias points the docker-agent config dir at a fresh tempdir
// and writes a single-alias config.yaml inside it. The override is
// reverted via t.Cleanup.
func writeAlias(t *testing.T, name, path string) {
	t.Helper()

	dir := t.TempDir()
	paths.SetConfigDir(dir)
	t.Cleanup(func() { paths.SetConfigDir("") })

	content := "aliases:\n  " + name + ":\n    path: " + path + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600))
}
