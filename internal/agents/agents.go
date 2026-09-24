// Package agents define los agentes de IA disponibles para la acción
// de ask AI: los built-in (opencode, pi, hermes) pueden
// reemplazarse desde el config global con [ask.agents.*].
//
// La plantilla de comando usa {prompt} como placeholder que ocupa un
// argumento argv completo (los prompts con espacios/comillas viajan
// como un solo argumento, sin interpretación de shell).
package agents

import (
	"os/exec"
	"strings"

	"vroom/internal/config"
)

// Agent es un agente de IA ejecutable.
type Agent struct {
	Name string   // identificador mostrado en el picker
	Cmd  []string // argv de la plantilla con {prompt} pendiente de expandir
}

// promptPlaceholder ocupa un argumento argv completo al expandir.
const promptPlaceholder = "{prompt}"

// Builtins devuelve los agentes por defecto con las invocaciones
// verificadas para "chat nuevo + prompt inicial". jcode no acepta prompt
// inicial en su TUI interactiva, así que usa `run` (one-shot: responde
// y termina).
func Builtins() []Agent {
	return []Agent{
		{Name: "opencode", Cmd: []string{"opencode", "--prompt", promptPlaceholder}},
		{Name: "pi", Cmd: []string{"pi", promptPlaceholder}},
		{Name: "hermes", Cmd: []string{"hermes", "chat", "-q", promptPlaceholder}},
		{Name: "jcode", Cmd: []string{"jcode", "run", promptPlaceholder}},
	}
}

// Resolve devuelve la lista final de agentes: los del config si la
// sección [ask.agents] tiene entradas; si no, los built-in.
func Resolve(overrides map[string]config.AgentConfig) []Agent {
	if len(overrides) == 0 {
		return Builtins()
	}
	// Orden alfabético por nombre para un picker determinista.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]Agent, 0, len(names))
	for _, name := range names {
		out = append(out, Agent{Name: name, Cmd: strings.Fields(overrides[name].Cmd)})
	}
	return out
}

// Available filtra los agentes cuyo binario (primer token del argv)
// está instalado en PATH.
func Available(list []Agent) []Agent {
	var out []Agent
	for _, a := range list {
		if len(a.Cmd) == 0 {
			continue
		}
		if _, err := exec.LookPath(a.Cmd[0]); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// BuildArgs expande la plantilla del agente con el prompt. El
// placeholder {prompt} se sustituye por el prompt completo como un
// único argumento; si el prompt está vacío el placeholder se elimina
// junto con el flag precedente (p.ej. "--prompt") para no dejar
// argumentos colgando.
func (a Agent) BuildArgs(prompt string) []string {
	args := make([]string, 0, len(a.Cmd))
	for _, part := range a.Cmd {
		if part == promptPlaceholder {
			if prompt == "" && len(args) > 0 && strings.HasPrefix(args[len(args)-1], "-") {
				args = args[:len(args)-1] // flag sin valor: fuera
			}
			if prompt != "" {
				args = append(args, prompt)
			}
			continue
		}
		args = append(args, part)
	}
	return args
}

// sortStrings ordena in situ (evita importar sort por tres líneas).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
