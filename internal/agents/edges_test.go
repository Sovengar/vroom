package agents

import (
	"os"
	"strings"
	"testing"
)

// Available filters by the INSTALLED BINARY, not by what the config says, which is why the check is on the first argv token: npx claude counts if npx exists; an agent with no command is not available, since offering it would error when pressed.
func TestAvailableFiltraPorBinarioInstaladoYNoDaErrores(t *testing.T) {
	// The test binary itself serves as the "installed" command, since it exists by definition.
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot resolve the binary itself: %v", err)
	}

	list := []Agent{
		{Name: "installed", Cmd: strings.Fields(self)},
		{Name: "installed-with-args", Cmd: append(strings.Fields(self), "--something")},
		{Name: "not-installed", Cmd: []string{"vroom-agent-that-never-exists-9911"}},
		{Name: "no-command", Cmd: nil},
		{Name: "empty-argv", Cmd: []string{}},
	}

	got := Available(list)

	want := []string{"installed", "installed-with-args"}
	if len(got) != len(want) {
		t.Fatalf("Available returned %d agents %v, want %d %v", len(got), names(got), len(want), want)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("Available[%d] = %q, want %q: the input order must be preserved", i, got[i].Name, want[i])
		}
	}

	if got := Available(nil); len(got) != 0 {
		t.Errorf("Available(nil) = %v, want empty", names(got))
	}
}

// A fresh slice is what lets the TUI keep the result and apply its own ordering without corrupting the config's list.
func TestAvailableDevuelveUnSliceNuevoNoElDeEntrada(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot resolve the binary itself: %v", err)
	}
	in := []Agent{
		{Name: "not-installed", Cmd: []string{"vroom-nonexistent-agent-9911"}},
		{Name: "installed", Cmd: strings.Fields(self)},
	}

	got := Available(in)
	if len(got) != 1 {
		t.Fatalf("Available returned %d, want 1", len(got))
	}
	if len(in) != 2 {
		t.Errorf("Available modified the input: %d agents instead of 2", len(in))
	}
	if &got[0] == &in[0] {
		t.Error("Available returned the same element as the input")
	}
}

// Sorting happens in place on the CALLER's slice, and that is part of the contract.
func TestSortStringsOrdenaInSituSinImportarSort(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"one", []string{"b"}, []string{"b"}},
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"reversed", []string{"c", "b", "a"}, []string{"a", "b", "c"}},
		{"interleaved", []string{"m", "a", "z", "b"}, []string{"a", "b", "m", "z"}},
		{"duplicates", []string{"b", "a", "b"}, []string{"a", "b", "b"}},
		{"uppercase first", []string{"b", "A"}, []string{"A", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := append([]string(nil), tt.in...)
			sortStrings(got)
			if len(got) != len(tt.want) {
				t.Fatalf("length %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("sortStrings(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestSortStringsOrdenaElSliceDelCaller(t *testing.T) {
	in := []string{"c", "a", "b"}
	sortStrings(in)
	if in[0] != "a" || in[1] != "b" || in[2] != "c" {
		t.Errorf("sortStrings did not sort the caller's slice: %v", in)
	}
}

func names(list []Agent) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Name)
	}
	return out
}
