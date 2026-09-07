package mise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMise(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// R28: parsea [tasks.*] con name/description y salta hide = true.
func TestTasksParse(t *testing.T) {
	dir := writeMise(t, `
[tools]
node = "22"

[tasks.cleancache]
run = "rm -rf .cache"
hide = true

[tasks.build]
description = 'Build the CLI'
run = "cargo build"
alias = 'b'

[tasks.test]
run = ["cargo test", "./scripts/test-e2e.sh"]

[tasks."test:unit"]
description = "unit tests"
run = "go test ./..."
`)
	tasks, err := Tasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d, want 3 (sin hide): %+v", len(tasks), tasks)
	}
	// Orden alfabético.
	want := []struct{ name, desc string }{
		{"build", "Build the CLI"},
		{"test", ""},
		{"test:unit", "unit tests"},
	}
	for i, w := range want {
		if tasks[i].Name != w.name || tasks[i].Description != w.desc {
			t.Errorf("tasks[%d] = %+v, want %s/%q", i, tasks[i], w.name, w.desc)
		}
	}
}

func TestTasksNoFile(t *testing.T) {
	if _, err := Tasks(t.TempDir()); err == nil {
		t.Error("sin mise.toml debe fallar")
	}
	if HasMiseToml(t.TempDir()) {
		t.Error("HasMiseToml = true sin fichero")
	}
}

func TestTasksMalformed(t *testing.T) {
	dir := writeMise(t, "esto no es [toml válido")
	if _, err := Tasks(dir); err == nil || !strings.Contains(err.Error(), "invalid mise.toml") {
		t.Errorf("err = %v, want invalid mise.toml", err)
	}
}

func TestTasksEmpty(t *testing.T) {
	dir := writeMise(t, "[tools]\nnode = \"22\"\n")
	tasks, err := Tasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %+v, want vacío", tasks)
	}
}
