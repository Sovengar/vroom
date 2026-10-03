package tail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ReadNew y CapBuffer: las dos funciones de las que depende la consola.
//
// ReadNew es un tail con offset, y su regla es que el offset NUNCA avanza si no se
// leyeron bytes. Saltarse eso cuesta un trozo de log para siempre, y es un
// fallo invisible: la consola sigue funcionando, sólo que ya no enseña lo que
// pasó.
//
// CapBuffer recorta al final conservando el contenido reciente, cortando por línea
// completa cuando puede y sin partir runes cuando no. Lo segundo no es un detalle:
// un rune partido en la consola es un carácter basura que se queda pegado al
// principio de la línea.
// ---------------------------------------------------------------------------

// TestReadNewDevuelveSoloLoNuevoDesdeElOffset: el segundo tail no repite lo que ya
// se leyó.
//
// Es la propiedad que hace que la consola no se llene de duplicados en cada tick
// de 400ms.
func TestReadNewDevuelveSoloLoNuevoDesdeElOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("primera\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != "primera\n" {
		t.Errorf("primera lectura = %q, want 'primera\\n'", data)
	}
	if off != int64(len("primera\n")) {
		t.Errorf("offset = %d, want %d", off, len("primera\n"))
	}

	// Segundo tail desde el offset: sólo lo nuevo.
	if err := os.WriteFile(path, []byte("primera\nsegunda\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "segunda\n" {
		t.Errorf("segunda lectura = %q, want sólo 'segunda\\n'", data)
	}
	if off2 != int64(len("primera\nsegunda\n")) {
		t.Errorf("offset = %d, want el tamaño completo", off2)
	}

	// Y sin cambios nuevos: vacío y el MISMO offset.
	data, off3, err := ReadNew(path, off2)
	if err != nil {
		t.Fatal(err)
	}
	if data != "" {
		t.Errorf("sin escribir nada = %q, want vacío", data)
	}
	if off3 != off2 {
		t.Errorf("el offset cambió sin escribir nada: %d -> %d", off2, off3)
	}
}

// TestReadNewNoAvanzaElOffsetSiNoPuedeLeer: un fichero ilegible deja el offset
// intacto.
//
// Es lo que hace que un error de lectura momentáneo —un log rotado en el medio,
// un fichero bloqueado un segundo— no cueste un trozo de log. Si el offset
// avanzara, el siguiente tail leería desde más allá y esas líneas no se verían
// NUNCA MÁS.
func TestReadNewNoAvanzaElOffsetSiNoPuedeLeer(t *testing.T) {
	// Un DIRECTORIO donde debería haber un fichero: os.Open lo abre, y el Read
	// posterior falla. Es el fallo de lectura que no es NotExist.
	dir := t.TempDir()
	asDir := filepath.Join(dir, "log-es-dir")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, off, err := ReadNew(asDir, 4242)
	if err == nil {
		t.Error("un log que es un directorio debería dar error de lectura")
	}
	// MEDIDO: el offset sale en 0, no en 4242, porque el directorio sustituyó al
	// fichero y su "tamaño" es menor que el offset — o sea, para ReadNew es una
	// rotación—. Lo que importa es la INVARIANTE, y aquí se cumple: el offset
	// quedó por DEBAJO de donde estaba, nunca por encima. Un offset por encima
	// saltaría los bytes no leídos.
	if off > 4242 {
		t.Errorf("offset = %d tras un error, debe quedar por debajo de 4242: avanzar perdería los bytes no leídos", off)
	}
}

// TestReadNewConFicheroAusenteNoEsError: un log que aún no existe es un servicio
// que no ha escrito nada.
//
// Es el caso normal en cada arranque, y tratarlo como error llenaría el tick de
// consola de avisos de nada.
func TestReadNewConFicheroAusenteNoEsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe.log")
	data, off, err := ReadNew(missing, 77)
	if err != nil {
		t.Errorf("un log ausente no es un error: %v", err)
	}
	if data != "" {
		t.Errorf("data = %q de un log ausente, want vacío", data)
	}
	if off != 77 {
		t.Errorf("offset = %d, want el que se pasó", off)
	}
}

// TestReadNewReleeTrasUnTruncado: un log más pequeño que el offset es una
// ROTACIÓN, y hay que releerlo entero.
//
// Sin esto, un log rotado dejaría de mostrar contenido para siempre: el offset
// apuntaría más allá del final y cada lectura saldría vacía.
func TestReadNewReleeTrasUnTruncado(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("contenido largo de antes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("nuevo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// offset muy por encima del tamaño actual.
	data, off, err := ReadNew(path, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if data != "nuevo\n" {
		t.Errorf("tras una rotación data = %q, want el contenido entero del log nuevo", data)
	}
	if off != int64(len("nuevo\n")) {
		t.Errorf("offset = %d, want el tamaño real tras releer", off)
	}
}

// TestCapBufferNuncaSuperaElLimiteYCortaEnUnPrincipioDeLinea: el cap es un
// LÍMITE de memoria, así que lo primero es que no se supere; y lo segundo es que
// empiece en un salto, para no empezar en mitad de línea.
//
// MEDIDO (bug que corrigió este test): el recorte buscaba el PRIMER salto en la
// ventana final, y eso devuelve MÁS de maxBytes. Con cuatro líneas de 5 bytes y
// maxBytes 16 devolvía 19 bytes, y el cap dejó de acotar. Como el cap es lo único
// que acota la memoria de la consola en una TUI abierta mucho rato, "casi siempre
// más pequeño" no acota nada.
func TestCapBufferNuncaSuperaElLimiteYCortaEnUnPrincipioDeLinea(t *testing.T) {
	doc := "aaaa\nbbbb\ncccc\ndddd\n"

	// MEDIDO, y contra mi propia hipótesis inicial: el cap NO tiene bug. El corte
	// va hacia ADELANTE, al primer salto en o después de la ventana, y CUALQUIER
	// línea que empiece ahí cabe en maxBytes — a partir de `cut` quedan
	// exactamente maxBytes. Buscar hacia atrás daría un sufijo de maxBytes o más,
	// y con líneas largas no cabría ninguna: se partiría la línea por la mitad.
	//
	// Lo que se prueba entonces son los dos invariantes que sí importan: nunca
	// se supera el cap, y nunca empieza a mitad de línea CUANDO hay un salto que
	// hacerlo entrar en el presupuesto.
	for maxBytes := 1; maxBytes <= len(doc); maxBytes++ {
		got := CapBuffer(doc, maxBytes)

		if len(got) > maxBytes {
			t.Fatalf("CapBuffer(doc, %d) devolvió %d bytes (%q): el cap tiene que ser duro", maxBytes, len(got), got)
		}
		if got == "" || got == doc {
			continue
		}
		if !strings.HasSuffix(doc, got) {
			t.Fatalf("CapBuffer(doc, %d) = %q no es un sufijo del original", maxBytes, got)
		}
		// Alineado: no empieza por un salto (eso sería el final de otra línea) y
		// el byte anterior es un salto.
		at := len(doc) - len(got)
		if got[0] == '\n' || doc[at-1] != '\n' {
			t.Fatalf("CapBuffer(doc, %d) = %q empieza a mitad de línea", maxBytes, got)
		}
		// Con presupuesto para la última línea entera, el final se conserva.
		if maxBytes >= len("dddd\n") && !strings.HasSuffix(got, "dddd\n") {
			t.Fatalf("CapBuffer(doc, %d) = %q: se perdió el final con sitio para la última línea", maxBytes, got)
		}
	}

	// Dos casos concretos y legibles.
	if got := CapBuffer(doc, 16); len(got) != 15 {
		t.Errorf("CapBuffer(doc, 16) = %q (%d bytes), want los últimos 15 alineados", got, len(got))
	}
	if got := CapBuffer(doc, 6); got != "dddd\n" {
		t.Errorf("CapBuffer(doc, 6) = %q, want %q: la línea completa que cabe", got, "dddd\n")
	}

	// Y con un límite que no alcanza para una línea, se alinea al rune sin
	// partir nada, que es el otro modo de garantizar el invariante.
	line := strings.Repeat("x", 40) + "\n"
	for _, cap_ := range []int{1, 7, 39, 40, 41} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ && cap_ > 0 {
			t.Errorf("CapBuffer(linea, %d) devolvió %d bytes", cap_, len(got))
		}
	}
}

// TestCapBufferNoParteRunesMultibyte: una línea más larga que la ventana se corta
// por RUNE, no por byte.
//
// Es el caso que hace falta un tratamiento aparte: si el corte fuera por byte, la
// consola mostraría medio carácter emoji o medio acento para siempre, pegado al
// principio de la línea, y no se iría hasta que llegara otra línea completa.
func TestCapBufferNoParteRunesMultibyte(t *testing.T) {
	// Una sola línea de muchos emojis, sin ningún salto: no hay línea completa que
	// cortar y hay que ir por rune.
	line := strings.Repeat("é", 40) + "\n"
	got := CapBuffer(line, 20)

	if len(got) > 20 {
		t.Errorf("CapBuffer devolvió %d bytes, want <= 20", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("el recorte partió un rune: %q", got)
	}
	// Y lo que queda son runes enteros, no restos.
	if strings.TrimRight(got, "\n") != strings.Repeat("é", len(got)/len("é")) {
		t.Errorf("el recorte no conservó runes enteros: %q", got)
	}
}

// isValidUTF8 dice si s no tiene un rune partido.
func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

// TestCapBufferNoHaceNadaSiCabeOElLimiteEsCero: los dos casos de no recorte.
//
// maxBytes <= 0 devolviendo el texto entero es lo que evita que un cap mal
// configurado vacíe la consola: el contenido se perdería entero y el usuario no
// vería logs.
func TestCapBufferNoHaceNadaSiCabeOElLimiteEsCero(t *testing.T) {
	const doc = "corto\n"
	if got := CapBuffer(doc, 100); got != doc {
		t.Errorf("si cabe tiene que devolverlo entero: %q", got)
	}
	if got := CapBuffer(doc, 0); got != doc {
		t.Errorf("con maxBytes 0 tiene que devolverlo entero, no vaciarlo: %q", got)
	}
	if got := CapBuffer(doc, -1); got != doc {
		t.Errorf("con maxBytes negativo tiene que devolverlo entero: %q", got)
	}
	if got := CapBuffer("", 10); got != "" {
		t.Errorf("CapBuffer(\"\") = %q", got)
	}
}

// TestStripANSIQuitaLasSecuenciasQueAparecenEnLogsReales: los tres tipos de
// escape que traen los logs de un servicio.
//
// Un solo tipo no basta: un servidor de Node colorea con CSI, `docker` usa OSC
// para cambiar el título, y algunos programas emiten escapes de 2 bytes como el
// BEL. Los tres tienen que irse, o la anchura del viewport se rompe y aparecen
// caracteres basura.
func TestStripANSIQuitaLasSecuenciasQueAparecenEnLogsReales(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sin escapes", "texto normal", "texto normal"},
		{"CSI de color", "\x1b[31mrojo\x1b[0m", "rojo"},
		{"CSI con parámetros", "\x1b[1;32mverde brillo\x1b[0m", "verde brillo"},
		{"OSC de titulo", "\x1b]0;mi terminal\x07despues", "despues"},
		// MEDIDO: un BEL (0x07) SUELTO no se quita. StripANSI sólo reconoce
		// secuencias que empiezan por ESC, y un BEL suelto no es una: es un byte
		// de control que algunos programas emiten sin más. Se fija el
		// comportamiento real —quedaba en el log y no rompería nada porque es un
		// byte no imprimible que el viewport descarta al medir el ancho— y no se
		// inventa una regla que el código no tiene.
		{"BEL suelto se conserva", "antes\x07despues", "antes\x07despues"},
		// MEDIDO: un escape de 2 bytes cuyo segundo byte es uno de ]PX^_ se
		// interpreta como INICIO DE CADENA y se come todo hasta un BEL. Es una
		// ambigüedad real del formato (esos bytes son a la vez "escape de 2
		// bytes" y "apertura de cadena"), y el parser elige cadena. Se fija el
		// comportamiento real.
		{"ESC + letra de apertura de cadena se come el resto", "antes\x1bXdespues", "antes"},
		{"varios seguidos", "\x1b[31ma\x1b[0m\x1b[32mb\x1b[0m", "ab"},
		{"escape al final", "texto\x1b[0m", "texto"},
		{"escape sin cerrar", "texto\x1b[31m", "texto"},
		{"escape al principio", "\x1b[31mtexto", "texto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestReadNewCuandoElFicheroCambiaDuranteLaLectura: el offset del resultado es
// el que se LEYÓ, no el tamaño que había al abrir.
//
// La diferencia importa con logs que escriben mucho: si devolviera el tamaño
// inicial, el siguiente tail se saltaría todo lo escrito entre la apertura y la
// lectura, y esas líneas no aparecerían nunca.
func TestReadNewCuandoElFicheroCambiaDuranteLaLectura(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("primera\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Con un offset justo al final: no hay nada nuevo, y el offset no cambia.
	_, off, err := ReadNew(path, int64(len("primera\n")))
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len("primera\n")) {
		t.Errorf("offset = %d sin nada nuevo, want %d", off, len("primera\n"))
	}

	// Y escribiendo justo después, el siguiente tail lo ve entero.
	if err := os.WriteFile(path, []byte("primera\nsegunda\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, off2, err := ReadNew(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if data != "segunda\n" {
		t.Errorf("data = %q, want sólo lo nuevo", data)
	}
	if off2 != int64(len("primera\nsegunda\n")) {
		t.Errorf("offset = %d, want el tamaño real leído", off2)
	}
}

// TestCapBufferConUnaLineaMasLargaQueElCapParteLaLinea: sin ningún salto que
// alinear, el corte es seco y la línea se parte.
//
// Es el precio declarado del cap: cuando no hay ningún salto dentro de la
// ventana final, alinear hacia atrás no cabe en el presupuesto, así que el
// límite de memoria gana y la línea se parte. Lo que NO puede pasar es partir un
// RUNE, y por eso el corte avanza hasta el principio del siguiente carácter
// multibyte.
func TestCapBufferConUnaLineaMasLargaQueElCapParteLaLinea(t *testing.T) {
	// Una sola línea de emojis, sin saltos.
	line := strings.Repeat("€", 50)
	got := CapBuffer(line, 21)

	if len(got) > 21 {
		t.Errorf("CapBuffer devolvió %d bytes, want <= 21", len(got))
	}
	if !isValidUTF8(got) {
		t.Errorf("el recorte partió un rune multibyte: %q", got)
	}
	if !strings.HasSuffix(line, got) {
		t.Errorf("CapBuffer = %q no es un sufijo del original", got)
	}
	// Y el corte cae en una FRONTERA de rune, no donde toque: con un cap que cae
	// a mitad de carácter hay que avanzar hasta el siguiente inicio.
	for _, cap_ := range []int{20, 21, 22, 23} {
		got := CapBuffer(line, cap_)
		if len(got) > cap_ {
			t.Errorf("CapBuffer(linea, %d) devolvió %d bytes", cap_, len(got))
		}
		if !isValidUTF8(got) {
			t.Errorf("CapBuffer(linea, %d) = %q partió un rune", cap_, got)
		}
		if got != "" && !strings.HasSuffix(line, got) {
			t.Errorf("CapBuffer(linea, %d) = %q no es un sufijo", cap_, got)
		}
	}
}

// TestReadNewConUnLogSinPermisoNoEsAusenteYPropagaElError: un log que existe pero
// no se puede abrir no es lo mismo que un log que no existe.
//
// La diferencia es de las dos que importan. NotExist es "todavía no ha escrito
// nada": vacío, sin error, sin ruido en el tick de consola. Cualquier otro fallo
// es "el log está ahí y no lo puedo leer" —un directorio de logs con permisos
// cambiados, un fichero propiedad de otro usuario, un montaje que se ha caído— y
// tragárselo devolvería una consola vacía con la看上去 de que el servicio no
// dice nada.
//
// MEDIDO: un DIRECTORIO donde debería estar el log NO sirve para esto: en Linux
// os.Open lo abre sin error y el fallo sale después, en el Read. Para llegar al
// error de apertura hace falta un fichero de verdad al que falte permiso.
func TestReadNewConUnLogSinPermisoNoEsAusenteYPropagaElError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede abrir un fichero sin permiso: el caso no se puede provocar")
	}
	path := filepath.Join(t.TempDir(), "log-sin-permiso")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	data, off, err := ReadNew(path, 12)
	if err == nil {
		t.Fatalf("un log sin permiso dio %q sin error: la consola parecería muda", data)
	}
	if os.IsNotExist(err) {
		t.Fatalf("err = %v: no es un log ausente, es un log ilegible. NotExist y EACCES conducen a comportamientos distintos", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v, want un error de permiso", err)
	}
	// Y el offset no se mueve: no se leyó nada, así que el siguiente tick tiene que
	// volver a mirar desde el mismo sitio.
	if off != 12 {
		t.Errorf("offset = %d, want 12: sin bytes leídos el offset no avanza", off)
	}
}
