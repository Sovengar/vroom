package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// The JSON is the agent-facing contract and it must mirror the manifest VERBATIM: same structure, same names, hooks included, so an agent never has to know whether this vroom understands a field.
func TestProjectInfoPublishesTheCommandsSectionVerbatim(t *testing.T) {
	p := scanner.Project{
		Name:       "actuacions-api",
		Path:       "/dev/actuacions-api",
		Configured: true,
		Manifest: &manifest.Manifest{
			Name: "actuacions-api",
			Commands: manifest.Commands{
				Start: manifest.StartCommand{
					Run:   "mise run start",
					Hooks: manifest.StartHooks{PreRun: "fuser -k 5005/tcp || true", PostRun: "curl -fsS localhost:8090/health"},
				},
				Build:   manifest.Runnable{Run: "mise run build"},
				Install: manifest.Runnable{Run: "mise run install"},
				Stop:    manifest.Runnable{Run: "docker stop actuacions-api"},
			},
		},
	}

	data, err := json.Marshal(buildProjectInfo(process.NewManager(), state.NewStoreAt(t.TempDir()), map[string]bool{}, p))
	if err != nil {
		t.Fatal(err)
	}

	var row map[string]any
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	if _, ok := row["commands"].(map[string]any); !ok {
		t.Fatalf("the row publishes no commands object: %s", data)
	}
	want := map[string]string{
		"commands.start.run":            "mise run start",
		"commands.start.hooks.pre_run":  "fuser -k 5005/tcp || true",
		"commands.start.hooks.post_run": "curl -fsS localhost:8090/health",
		"commands.build.run":            "mise run build",
		"commands.install.run":          "mise run install",
		"commands.stop.run":             "docker stop actuacions-api",
	}
	for path, expect := range want {
		if got := dig(row, strings.Split(path, ".")); got != expect {
			t.Errorf("%s = %v, want %q", path, got, expect)
		}
	}
}

// dig walks the dotted path a manifest uses, so the assertion reads exactly like the TOML key it mirrors.
func dig(node any, path []string) any {
	current := node
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}
