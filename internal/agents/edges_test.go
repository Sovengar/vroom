package agents

import (
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Available y sortStrings: la lista de agentes que la TUI puede ofrecer y el orden
// en que aparecen.
//
// Available filtra por BINARIO INSTALADO, no por lo que el config diga. Eso
// significa que la lista depende de la máquina, y es la razón de que el filtro
// sea por el primer token del argv: `npx claude` está disponible si hay npx, y
// un config con un comando que no existe en esta máquina tiene que desaparecer
// de la lista en vez de fallar al ejecutarse.
//
// Y un agente sin comando NO es un agente disponible: es un config mal escrito,
// y ofrecerlo produciría un error al pulsarlo.
// ---------------------------------------------------------------------------

// TestAvailableFiltraPorBinarioInstaladoYNoDaErrores: sólo pasan los agentes
// cuyo primer token existe en el PATH.
func TestAvailableFiltraPorBinarioInstaladoYNoDaErrores(t *testing.T) {
	// Se usa el propio binario de test, que existe por definición.
	self, err := os.Executable()
	if err != nil {
		t.Skipf("no se puede resolver el propio binario: %v", err)
	}

	list := []Agent{
		{Name: "instalado", Cmd: strings.Fields(self)},
		{Name: "instalado-con-args", Cmd: append(strings.Fields(self), "--algo")},
		{Name: "no-instalado", Cmd: []string{"vroom-agente-que-no-existe-nunca-9911"}},
		{Name: "sin-comando", Cmd: nil},
		{Name: "argv-vacio", Cmd: []string{}},
	}

	got := Available(list)

	want := []string{"instalado", "instalado-con-args"}
	if len(got) != len(want) {
		t.Fatalf("Available devolvió %d agentes %v, want %d %v", len(got), names(got), len(want), want)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("Available[%d] = %q, want %q: el orden de la entrada tiene que conservarse", i, got[i].Name, want[i])
		}
	}

	// Y una lista vacía o nula da vacío, no un error.
	if got := Available(nil); len(got) != 0 {
		t.Errorf("Available(nil) = %v, want vacío", names(got))
	}
}

// TestAvailableDevuelveUnSliceNuevoNoElDeEntrada: la lista filtrada no puede ser
// el mismo slice que la de entrada.
//
// Es lo que permite que la TUI guarde el resultado y aplique el orden de sus
// propios criterios sin que se corrompa la lista de la config.
func TestAvailableDevuelveUnSliceNuevoNoElDeEntrada(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("no se puede resolver el propio binario: %v", err)
	}
	in := []Agent{
		{Name: "no-instalado", Cmd: []string{"vroom-agente-inexistente-9911"}},
		{Name: "instalado", Cmd: strings.Fields(self)},
	}

	got := Available(in)
	if len(got) != 1 {
		t.Fatalf("Available devolvió %d, want 1", len(got))
	}
	if len(in) != 2 {
		t.Errorf("Available modificó la entrada: %d agentes en vez de 2", len(in))
	}
	// Y el slice de salida no comparte backing con el de entrada.
	if &got[0] == &in[0] {
		t.Error("Available devolvió el mismo elemento que la entrada")
	}
}

// TestSortStringsOrdenaInSituSinImportarSort: la ordenación es una burbuja
// sobre el slice DEL CALLER, y eso es parte del contrato.
//
// Se comprueba con tres casos que entre ellos cubren el algoritmo entero: ya
// ordenado (ningún intercambio), al revés (máximo de intercambios) y con un
// duplicado. Y que ordena por bytes, que es lo que hace que la lista sea
// estable entre máquinas.
func TestSortStringsOrdenaInSituSinImportarSort(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"vacío", nil, nil},
		{"uno", []string{"b"}, []string{"b"}},
		{"ya ordenado", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"al revés", []string{"c", "b", "a"}, []string{"a", "b", "c"}},
		{"intercalado", []string{"m", "a", "z", "b"}, []string{"a", "b", "m", "z"}},
		{"duplicados", []string{"b", "a", "b"}, []string{"a", "b", "b"}},
		{"mayúsculas antes", []string{"b", "A"}, []string{"A", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := append([]string(nil), tt.in...)
			sortStrings(got)
			if len(got) != len(tt.want) {
				t.Fatalf("longitud %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("sortStrings(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

// TestSortStringsOrdenaElSliceDelCaller: in situ de verdad, que es lo que permite
// ordenarlo sin reservar un slice nuevo en cada arranque de la TUI.
func TestSortStringsOrdenaElSliceDelCaller(t *testing.T) {
	in := []string{"c", "a", "b"}
	sortStrings(in)
	if in[0] != "a" || in[1] != "b" || in[2] != "c" {
		t.Errorf("sortStrings no ordenó el slice del caller: %v", in)
	}
}

func names(list []Agent) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Name)
	}
	return out
}
