# Lenguaje: Gherkin. Comportamiento esperado, no steps de Cucumber.
# Este archivo es la UNICA fuente de verdad del comportamiento. El ejecutor
# lo convierte en tests reales (Go, `make check`).
#
# Alcance: SLICES 1-3 (el nucleo liberable, sin portless).
# El slice 4 (portless) vive en behavior-portless.feature: es un PR aparte.

# ---------------------------------------------------------------------------
# SLICE 1 — Stop por linaje + guarda de propiedad de killPortHolder
# ---------------------------------------------------------------------------
Feature: Stop por linaje no deja procesos huerfanos ni mata procesos ajenos

  Background:
    Given un servicio vroom arrancado con exito y registrado con su PID, PGID y
      creation_time

  Scenario: Stop mata a un descendiente que se ha re-sid
    Given el proceso hijo principal arranca su propio backend con "setsid",
      de modo que el backend queda en otro process group
    When el usuario detiene el servicio
    Then el proceso principal termina
    And el backend tambien termina
    And ningun PID del linaje sobrevive al stop
    And el puerto que el backend escuchaba queda libre

  Scenario: Stop sigue funcionando en el caso normal de un solo process group
    Given el servicio arranca un proceso que NO llama a setsid
    And todos sus hijos siguen en el process group registrado
    When el usuario detiene el servicio
    Then el process group entero termina
    And el puerto configurado queda libre
    And el servicio queda registrado como detenido

  Scenario: Stop NO mata a un proceso ajeno que ocupa el puerto
    Given el servicio ya esta detenido y su PID fue limpiado
    And OTRO servicio, de otro worktree, esta escuchando en el mismo puerto
    When el usuario detiene el servicio que ya estaba detenido
    Then el proceso del otro worktree sigue vivo
    And el puerto sigue abierto
    And vroom NO ejecuta "fuser -k" sobre ese puerto

  Scenario: Stop con propietario del puerto desconocido falla de forma cerrada
    Given el proceso que escucha en el puerto pertenece a otro lineage
    And vroom no puede determinar quien lo ocupa
    When el usuario detiene el servicio
    Then vroom NO mata a ningun proceso del puerto
    And vroom muestra un aviso explicito de que no pudo liberar el puerto
    And el servicio queda registrado como detenido igualmente

  Scenario: Stop es idempotente
    Given el servicio ya fue detenido con exito
    When el usuario detiene el servicio de nuevo
    Then el comando termina sin error
    And no se intenta matar nada ajeno

# ---------------------------------------------------------------------------
# SLICE 2 — Puertos dinamicos: reserva, inyeccion, descubrimiento, verdad unica
# ---------------------------------------------------------------------------

Feature: Puertos dinamicos por worktree con el puerto real como unica verdad

  Scenario: Un manifiesto sin port_mode se comporta exactamente como hoy
    Given un manifiesto existente que declara "port = 8080" y nada mas
    When el usuario arranca el servicio
    Then vroom no reserva ningun puerto
    And vroom no inyecta ninguna variable de puerto en el hijo
    And el puerto del proceso es el 8080
    And el puerto mostrado, el del JSON y el de la sonda de salud coinciden

  Scenario: Modo none equivale al port = 0 actual
    Given un manifiesto con "port_mode = \"none\""
    When el usuario arranca el servicio
    Then el servicio arranca sin puerto
    And el estado y el JSON reportan que no hay puerto
    And vroom no muestra "0" como si fuera un puerto real

  Scenario: Arranque dinamico, la app honra el puerto inyectado
    Given un manifiesto con "port_mode = \"dynamic\"" y "port = 8080"
    And la app lee su puerto como "PORT=${PORT:-8080}"
    When el usuario arranca el servicio
    Then vroom reserva un puerto libre dentro del rango 4000-4999
    And el puerto aparece en el entorno del proceso hijo
    And el puerto real del proceso es el puerto reservado
    And el puerto se persiste antes de que el arranque devuelva el control
    And el puerto mostrado, el del JSON y el de la sonda de salud son ese mismo

  Scenario: El entorno del hijo NO se trunca
    Given un manifiesto en modo dinamico
    And el usuario tiene PATH, HOME y otras variables en su entorno
    When el usuario arranca el servicio
    Then el proceso hijo hereda el entorno completo del padre
    And ademas recibe el puerto inyectado
    And el hijo puede resolver comandos como "sh", "node" o "mise" por su PATH

  Scenario: La ventana de arranque nunca reporta un proceso vivo como detenido
    Given un servicio en modo dinamico cuyo arranque tarda en resolverse el puerto
    And el tick de la TUI se ejecuta durante esa ventana
    Then el servicio se muestra como arrancando con el puerto pendiente
    And NO se muestra como detenido mientras su proceso siga vivo
    And en cuanto el puerto se resuelve, el estado pasa a vivo

  Scenario: La app ignora el puerto reservado
    Given un manifiesto en modo dinamico
    And la app ignora la variable PORT y hace bind a su propio puerto fijo
    When el usuario arranca el servicio
    Then el arranque NO falla
    And el puerto real descubierto es el de la app
    And vroom emite un AVISO visible de que la app no tomo el puerto ofrecido
    And el aviso no es un error ni impide operar el servicio

  Scenario: Un servicio que muere al arrancar se detecta rapido
    Given un servicio en modo dinamico que falla y muere de inmediato
    When el usuario arranca el servicio
    Then vroom lo reporta como fallo de arranque
    And el tiempo hasta el reporte es acotado, del orden de 1 segundo
    And vroom NO espera a agotar el timeout del discovery

  Scenario: Dos arranques concurrentes no comparten puerto
    Given un manifiesto en modo dinamico
    And dos servicios que se arrancan A LA VEZ (una etapa de stack, o un
      nodo de grupo al que se pulsa una vez)
    When ambos reservan su puerto al mismo tiempo
    Then vroom devuelve dos puertos distintos
    And ninguno de los dos arranca en el puerto del otro
    And un arranque que falla devuelve su puerto al pool
    And al DETENER un servicio su puerto vuelve al pool
    And un servicio que se arranca y se para repetidamente no encoge el rango
    And un reinicio de la TUI empieza con el rango entero disponible

  Scenario: Un servicio solo-UDP no cuelga el arranque
    Given un servicio en modo dinamico que nunca abre un puerto TCP
    When el usuario arranca el servicio
    Then vroom registra explicitamente que el servicio no tiene puerto
    And el arranque NO se queda esperando indefinidamente
    And el servicio queda igualmente operable

  Scenario: Un servicio lento conserva su puerto
    Given un servicio en modo dinamico que tarda 3.5 segundos en hacer bind
    When el usuario arranca el servicio
    Then el puerto se resuelve correctamente
    And el servicio NO se reporta como "sin puerto"

  Scenario: Vencido el plazo, vroom reintenta antes de rendirse
    Given un servicio en modo dinamico que hace bind despues del plazo de
      discovery pero dentro de una segunda ventana acotada
    When el usuario arranca el servicio
    Then el puerto se resuelve igualmente y queda verificado
    And NO queda en estado "puerto sin resolver"

  Scenario: Un puerto sin resolver no es lo mismo que no tener puerto
    Given un servicio en modo dinamico que abrio listeners pero su conjunto
      nunca se estabiliza, asi que no se puede declarar ninguno principal
    When el usuario mira el estado y la tab de salud
    Then vroom NO lo etiqueta como "sin puerto"
    And vroom dice explicitamente que el puerto esta sin resolver
    And NO muestra el puerto declarado como si fuera el suyo
    And la sonda de salud NO se ejecuta contra el puerto declarado
    And el servicio sigue siendo detenible
    And un reinicio del servicio vuelve a intentar descubrir el puerto

  Scenario: El puerto mostrado y el de la sonda de salud son el mismo
    Given un servicio en modo dinamico ya arrancado con un puerto real
    When el usuario abre la vista de servicio, el dashboard, la tab de health
      y el JSON de la CLI
    Then todos muestran el mismo puerto real
    And ninguno muestra el puerto declarado en el manifiesto
    And la sonda HTTP se ejecuta contra ese mismo puerto

  Scenario: Tras detener, el puerto vuelve a ser el declarado
    Given un servicio en modo dinamico ya detenido
    When el usuario mira el estado y el JSON
    Then ya no se muestra el puerto efimero de la corrida anterior
    And el puerto mostrado vuelve a ser el del manifiesto

  Scenario: Un puerto que cambia bajo una sonda en vuelo no mezcla generaciones
    Given un servicio que se reinicia y cambia de puerto
    And una sonda de salud esta en curso mientras el puerto cambia
    Then el estado y la sonda se refieren al mismo proceso
    And vroom no informa "vivo y sano" usando un puerto de otra corrida

  Scenario: La propiedad del puerto se resuelve sin falsos positivos
    Given dos servicios que declaran el mismo puerto, cada uno en su worktree
    And solo uno de los dos esta vivo
    When vroom evalua el estado del servicio que esta parado
    Then vroom NO lo reporta como vivo por el hecho de que el otro escucha
    And cuando el propietario del puerto no se puede determinar,
      vroom NO resuelve a "vivo" por defecto

  Scenario: El puerto con IPv6 no confunde la propiedad
    Given un host donde el mismo puerto se resuelve por IPv4 y por IPv6
    And el proceso de otro servicio escucha en una de esas dos direcciones
    When vroom determina quien escucha en el puerto
    Then vroom distingue las direcciones de bind
    And NO atribuye al servicio un proceso que escucha en otra direccion

  Scenario: El estado persistido del puerto sobrevive entre ticks
    Given un servicio en modo dinamico ya arrancado
    When otro proceso lee el estado persistido del servicio
    Then el estado persistido indica el puerto real resuelto
    And ese estado persistido es consumido, no ignorado

  Scenario: El estado pendiente de puerto no se confunde con vivo-sano
    Given un servicio cuyo puerto aun no se resolvio
    Then la interfaz NO lo presenta como un servicio sano
    And el servicio sigue siendo detenible por el usuario

  Scenario: La validacion del manifiesto aplica la regla de campos cruzados
    Given un manifiesto que declara health_path pero no tiene puerto en ningun modo
    When el usuario valida o arranca la configuracion
    Then vroom rechaza la configuracion con un error claro
    And un manifiesto con health_path y puerto es aceptado
    And un manifiesto con health_path y "port_mode = \"none\"" es rechazado

  Scenario: Modo fixed con port = 0 avanza exactamente como hoy
    Given un stack con una etapa en modo fixed y "port = 0"
    When el motor espera la salud de esa etapa
    Then la espera se comporta EXACTAMENTE como hoy: una espera breve y retorno ok
    And la etapa avanza sin verificar puerto
    And no aparece ningun estado nuevo
    And no se emite ningun aviso
    And no hay ninguna retencion extra

  Scenario: Modo fixed con puerto concreto gatea contra el puerto del manifiesto
    Given un stack con una etapa en modo fixed y "port = 8080"
    When el motor espera la salud de esa etapa
    Then la espera gatea contra el puerto del manifiesto, igual que hoy
    And si el puerto no abre dentro del timeout, la etapa falla como hoy
    And no aparece ningun estado nuevo ni aviso

  Scenario: Modo fixed con port_mode ausente se comporta como fixed
    Given un manifiesto que no declara port_mode en absoluto
    And una etapa de stack con puerto declarado
    When el motor espera la salud de esa etapa
    Then el comportamiento es indistinguible del de un manifiesto que si declara
      "port_mode = \"fixed\""
    And en ningun punto del camino se consulta una reserva de puerto

  Scenario: Modo none en una etapa de stack no espera puerto
    Given un stack con una etapa en "port_mode = \"none\""
    When el motor espera la salud de esa etapa
    Then la etapa avanza sin espera de puerto, porque el modo lo declara
      explicitamente
    And vroom distingue "no tiene puerto por diseno" de "el puerto sigue sin
      resolverse"

  Scenario: Modo dynamic con discovery en vuelo no avanza por timeout en crudo
    Given un stack con una etapa en modo dinamico cuyo puerto esta reservado
    And el discovery de esa etapa sigue en vuelo
    When la espera de salud agota su presupuesto
    Then la etapa NO avanza dândoselo por bueno
    And la espera consume su presupuesto dentro del discovery en curso
    And el resultado se reporta de forma distinta: puerto pendiente o fallo
    And la causa se distingue de un simple timeout de puerto

  Scenario: Modo dynamic sin puerto nunca termina en espera infinita
    Given un stack con una etapa en modo dinamico donde no aparece ningun puerto
    TCP, por ejemplo porque el servicio es solo UDP
    And esa etapa comparte etapa con otro servicio que SI abre puerto
    When el motor espera la salud de esa etapa
    Then la espera termina de forma acotada
    And NUNCA se queda colgada hasta un timeout crudo
    And el resultado se reporta como "sin puerto"
    And NO se reporta como un error de arranque
    And la launch NO aborta ni hace rollback de los ya arrancados
    And el hermano de la misma etapa sigue corriendo
    And el servicio sin puerto queda igualmente operable y detenible

  Scenario: Un puerto sin resolver no hace fracasar la etapa
    Given una etapa de stack con un servicio cuyo puerto quedo sin resolver
      y cuyo discovery YA TERMINO
    And esa etapa comparte etapa con un hermano sano que se acaba de arrancar
    When el motor espera la salud de esa etapa
    Then la etapa NO falla
    And el resultado NO dice "pending", porque nada mas va a cambiar
    And el resultado se distingue de "sin puerto TCP" y de "resuelto"
    And NO se dispara el rollback de los ya arrancados
    And el hermano sano sigue corriendo
    And el servicio con el puerto sin resolver sigue vivo y detenible

  Scenario: Un puerto PENDIENTE si hace fracasar la etapa
    Given una etapa de stack con un servicio cuyo discovery sigue en vuelo
      y cuyo puerto esta reservado pero todavia no escucha
    When el motor espera la salud de esa etapa
    Then la etapa falla
    And la causa se nombra como puerto pendiente
    And NO se confunde con un puerto ya sin resolver

  Scenario: La regla de retencion solo aplica en modo dynamic
    Given un stack con una etapa en modo fixed cuyo puerto nunca abre
    When el motor espera la salud de esa etapa
    Then la etapa falla exactamente como hoy, sin el tratamiento de puerto
      pendiente introducido para el modo dynamic
    And el modo fixed no gana ninguna semantica nueva por el hecho de existir
      port_mode

# ---------------------------------------------------------------------------
# SLICE 3 — Desambiguacion multi-puerto (R1 / R2 / R3)
# ---------------------------------------------------------------------------

Feature: Eleccion determinista del puerto principal cuando hay varios listeners

  Scenario: R1 — el puerto reservado es el principal
    Given un servicio que honra PORT y ademas abre un puerto de metrics
      antes que el principal
    When vroom descubre los listeners
    Then vroom elige el puerto reservado
    And la eleccion es determinista y no usa heuristica
    And el servicio se marca con puerto verificado

  Scenario: R2 — la app ignora PORT y health_path decide
    Given un servicio que ignora PORT y abre dos listeners
    And solo uno responde bien en health_path
    When vroom descubre los listeners
    Then vroom elige el listener que mejor responde en health_path
    And la eleccion es determinista

  Scenario: R3 — empate o protocolo no-HTTP, gana el menor puerto
    Given un servicio no-HTTP que abre varios listeners e ignora PORT
    When vroom descubre los listeners
    Then vroom elige el de menor numero de puerto
    And la eleccion es determinista entre corridas
    And vroom marca el servicio como "puerto no verificado"
    And vroom declara explicitamente que no puede saber cual es el principal

  Scenario: Un unico listener es trivialmente el principal
    Given un servicio en modo dinamico que abre un solo listener
    When vroom descubre los listeners
    Then ese listener es el puerto del servicio
    And el servicio se marca con puerto verificado

  Scenario: El discovery no se ejecuta en el tick de la interfaz
    Given un servicio ya arrancado cuyo puerto esta resuelto
    When la interfaz refresca el estado repetidamente
    Then el descubrimiento completo NO se ejecuta en cada tick
    And el coste por tick se mantiene acotado e independiente del numero de
      listeners observados