# Lenguaje: Gherkin. Comportamiento esperado, no steps de Cucumber.
#
# SLICE 4 — vroom REGISTRA rutas en portless. vroom NO gestiona el proxy.
#
# DECISION DEL USUARIO, intacta e innegociable:
#   vroom SÓLO registra alias. NUNCA arranca, gestiona, supervisa ni muestra el
#   proxy. Si no hay proxy alcanzable, vroom avisa UNA vez y continúa: el servicio
#   sigue funcionando en su propio puerto, sólo que sin URL estable.
#   LA SALUD DE UN SERVICIO NUNCA DEPENDE DE QUE EXISTA SU RUTA.
#   Una ruta es una dirección, no una dependencia.
#
# ---------------------------------------------------------------------------
# Hechos MEDIDOS que este archivo da por ciertos (portless 0.15.6 / Node 24.15.0).
# Ninguno es una suposición; todos tienen comando y salida en context.md §7.
# ---------------------------------------------------------------------------
#   M1  alias es una escritura PURA del fichero de estado: NUNCA contacta con el
#       proxy. Con el proxy caído, alias sale con exit 0 y escribe la ruta igual.
#       => exit 0 NO prueba que la URL resuelva ahora mismo.
#   M2  Leer routes.json de vuelta sólo prueba que escribimos. La verificación
#       tiene que hacerse CONTRA EL PROXY VIVO.
#   M3  La ruta sobrevive a un reinicio del proxy: routes.json es el fichero de
#       estado declarado del proxy y se recarga. MEDIDO.
#   M4  Escribir un alias NO daña las rutas vivas que gestiona portless: una ruta
#       con pid propio sobrevive intacta y sigue sirviendo. MEDIDO.
#   M5  prune NO toca rutas de alias (pid: 0, contadas como activas). MEDIDO.
#       Corolario: vroom es lo UNICO que puede limpiarlas.
#   M6  proxy.port SOLO existe mientras el proxy corre. Su ausencia ES la señal
#       de que no hay proxy. Nunca se asume 1355.
#   M7  Un nombre con punto se acepta y se conserva literal.
#   M8  El registro es un UPSERT INCONDICIONAL: mismo nombre + otro puerto sale
#       con exit 0 y SOBREESCRIBE en silencio. No hay detección de conflictos.
#   M9  El puerto destino NO tiene que estar escuchando; el proxy responde 502.
#   M10 --remove de un nombre inexistente sale con exit 1 y es BENIGNO.
#   M11 Una app que falla bajo 'portless run' imprime la URL pero NO registra
#       ruta. Es el espejo del comportamiento correcto.
#
# Sobre hermeticidad: el runner de CI no tiene portless, ni node 24, ni proxy.
# Por defecto TODA la suite usa un seam inyectado con fixtures. A lo sumo UN test
# de integracion, marcado, skipped si no hay portless real.

Feature: vroom publica una ruta estable en portless sin poseer el proxy

  # ==================================================================
  # 1. El criterio que gobierna todo el archivo
  # ==================================================================

  Scenario Outline: La salud del servicio no depende de la ruta
    Given un servicio <modo> con puerto
    And el resultado de la ruta es <resultado_de_ruta>
    When el usuario consulta el estado del servicio
    Then el estado del servicio es <estado_de_servicio>
    And el veredicto de salud no menciona la ruta
    And el arranque no se considera fallido por el resultado de la ruta

    Examples:
      | modo    | resultado_de_ruta           | estado_de_servicio |
      | dynamic | registered                  | running            |
      | dynamic | degraded (portless ausente) | running            |
      | dynamic | degraded (sin proxy)        | running            |
      | dynamic | degraded (no verificable)   | running            |
      | dynamic | degraded (conflicto)        | running            |
      | dynamic | degraded (port_unresolved)  | running            |
      | fixed   | registered                  | running            |
      | fixed   | degraded (alias falla)      | running            |

  # ==================================================================
  # 2. Ausencia total: la degradacion es el caso NORMAL
  # ==================================================================

  Scenario: portless no esta en el PATH
    Given un servicio en modo dynamic con puerto
    And el seam de portless reporta el binario como no encontrado
    When el usuario arranca el servicio
    Then el arranque TIENE EXITO
    And el puerto se reserva, se inyecta y se descubre igual que sin portless
    And no se invoca a portless
    And se emite un aviso de que la capa de proxy no esta disponible
    And el estado del servicio es running
    And el JSON no publica ninguna ruta

  Scenario: portless esta presente pero no funciona
    Given un servicio en modo dynamic con puerto
    And el seam de portless devuelve un binario que sale con codigo distinto de cero
    When el usuario arranca el servicio
    Then el arranque TIENE EXITO
    And se emite un aviso que nombra el fallo concreto del binario
    And el servicio queda running
    And el JSON no publica ninguna ruta

  Scenario: portless esta presente pero se queda colgado
    Given un servicio en modo dynamic con puerto
    And el seam de portless devuelve un binario que no termina nunca
    When el usuario arranca el servicio
    Then la llamada a portless esta ACOTADA por un timeout
    And el arranque TIENE EXITO tras vencer ese timeout
    And vroom no se queda bloqueado esperando al binario
    And se emite un aviso de que portless no respondio

  Scenario: portless esta presente pero su node es demasiado antiguo
    Given un servicio en modo dynamic con puerto
    And portless se niega a ejecutar porque node es menor de 24
    When el usuario arranca el servicio
    Then el arranque TIENE EXITO
    And vroom NO modifica la configuracion de node del usuario
    And vroom no escribe ni cambia ningun default global
    And el servicio queda running

  # ==================================================================
  # 3. Proxy ausente: vroom avisa, NO lo arranca
  # ==================================================================

  Scenario: portless instalado pero sin proxy en marcha
    Given un servicio en modo dynamic con puerto
    And portless esta disponible
    And el fichero que declara el puerto del proxy NO EXISTE
    When el usuario arranca el servicio
    Then vroom NO intenta arrancar el proxy
    And vroom NO pide privilegios de superusuario
    And vroom NO supone ningun puerto por defecto para el proxy
    And el arranque TIENE EXITO y el servicio queda running
    And se avisa UNA vez de que no hay proxy alcanzable
    And el JSON no publica ninguna ruta

  Scenario: el puerto del proxy se declara pero no acepta conexiones
    Given un servicio en modo dynamic con puerto
    And el fichero que declara el puerto del proxy existe
    And ese puerto no acepta ninguna conexion
    When el usuario arranca el servicio
    Then vroom NO intenta arrancar el proxy
    And el arranque TIENE EXITO y el servicio queda running
    And el JSON no publica ninguna ruta
    And el aviso explica que el proxy declarado no responde

  Scenario: el proxy no es un servicio mas de vroom
    Given un servicio en modo dynamic con ruta registrada
    When el usuario ve la lista de servicios de vroom
    Then el proxy no aparece en la lista
    And el usuario no puede detener el proxy por la via normal de un servicio

  # ==================================================================
  # 4. El camino feliz: la ruta apunta al puerto REAL y esta verificada
  # ==================================================================

  Scenario: Puerto descubierto, ruta registrada y verificada en el proxy vivo
    Given un servicio en modo dynamic con puerto
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then se registra una ruta apuntando al puerto real del servicio
    And la ruta se verifica CONTRA EL PROXY VIVO antes de publicarse
    And el estado del servicio es running
    And el JSON publica la ruta con estado registered
    And el JSON publica una url que responde

  Scenario: La app ignora el puerto reservado y hace bind en otro sitio
    Given un servicio en modo dynamic con puerto
    And la app ignora el PORT que vroom le inyecta y hace bind en otro puerto
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then la ruta apunta al puerto que la app REALMENTE escucha
    And la ruta NO apunta al puerto reservado
    And se avisa de que la app ignoro el puerto ofrecido
    And el estado del servicio es running

  Scenario: El puerto todavia no escucha cuando toca registrar
    Given un servicio en modo dynamic con puerto
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then la ruta se registra DESPUES de que el discovery confirme el puerto real
    And no se registra ninguna ruta mientras el puerto este sin resolver
    And una ruta contra un puerto cerrado no se reporta como servicio listo
    And el servicio NO se declara sano por el hecho de tener ruta

  Scenario: Un servicio sin puerto no recibe ruta
    Given un servicio cuyo puerto real no se puede resolver
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then no se registra ninguna ruta
    And el JSON no publica ninguna url
    And el motivo publicado dice que no se pudo resolver el puerto

  # ==================================================================
  # 5. La verificacion: contra el proxy VIVO, no contra el fichero
  #    Este bloque es el que M1 y M2 obligan a escribir.
  # ==================================================================

  Scenario: La ruta esta en el fichero pero el proxy esta caido
    Given un servicio en modo dynamic con puerto
    And portless esta disponible
    And la ruta esta escrita en el fichero de estado de portless
    And el proxy NO esta sirviendo
    When el usuario arranca el servicio
    Then vroom NO reporta la ruta como disponible
    And el estado publicado es degraded con motivo de proxy que no responde
    And el JSON NO publica ninguna url
    And una lectura del fichero de estado NO cuenta como verificacion
    And el estado del servicio es running

  Scenario: El proxy responde por la ruta y eso es prueba suficiente
    Given un servicio en modo dynamic con puerto
    And la ruta esta registrada y el proxy responde por ella
    When el usuario arranca el servicio
    Then vroom acepta que la ruta esta disponible
    And el estado publicado es registered
    And se publica una url

  Scenario: El proxy responde con error de puerta trasera y aun asi enruta
    Given un servicio en modo dynamic con puerto
    And la ruta esta registrada
    And el proxy enruta la ruta pero el servicio de detras no responde
    When el usuario verifica la ruta
    Then vroom tiene por CORRECTO que el proxy enruta esa ruta
    And el resultado de la verificacion distingue "el proxy enruta" de "el servicio responde"

  Scenario: El esquema de la url se determina probando, no suponiendo
    Given un servicio en modo dynamic con puerto
    And la ruta esta registrada en un proxy cuyo esquema vroom no conoce de antemano
    When el usuario arranca el servicio
    Then vroom comprueba el esquema y publica el que responde
    And vroom NO publica un esquema que no ha comprobado
    And si ninguno responde, no se publica ninguna url

  Scenario: El servicio cae justo despues de arrancar
    Given un servicio en modo dynamic con puerto
    And la ruta se verifico correctamente en un instante anterior
    And el servicio muere despues de esa verificacion
    When el usuario consulta el JSON
    Then la ultima ruta conocida se conserva como ultimo estado conocido
    And no se afirma que la ruta este sana en este momento

  # ==================================================================
  # 6. La lectura de vuelta: exit 0 NO es prueba de propiedad
  # ==================================================================

  Scenario: El nombre de la ruta ya lo tiene otro servicio en otro puerto
    Given un servicio en modo dynamic con puerto
    And el nombre de la ruta ya esta registrado en portless apuntando a OTRO puerto
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then vroom LEE DE VUELTA la ruta despues de registrarla
    And detecta que el puerto de la ruta no es el suyo
    And NO reporta la ruta como registrada
    And el estado publicado es degraded con motivo de conflicto
    And se avisa de que el nombre esta tomado por otro puerto
    And el estado del servicio es running

  Scenario: La lectura de vuelta confirma que la ruta es nuestra
    Given un servicio en modo dynamic con puerto
    And portless esta disponible y el proxy responde
    When el usuario arranca el servicio
    Then la lectura de vuelta encuentra la ruta con el puerto esperado
    And el estado publicado es registered
    And se publica una url

  Scenario: vroom nunca afirma una ruta que no puede ver servir
    Given un servicio en modo dynamic con puerto
    And tras registrarse, la ruta no responde por el proxy vivo
    When el usuario consulta el JSON
    Then el estado publicado NO es registered
    And no se publica ninguna url

  Scenario: Escribir la ruta de vroom no daña las rutas que gestiona portless
    Given una app viva arrancada con portless, con su propia ruta y su proceso
    And un servicio de vroom con ruta en modo dynamic
    When vroom registra la ruta de su servicio
    Then la ruta de la app viva sigue presente y sigue respondiendo
    And el proceso de la app viva sigue vivo
    And vroom no evacua ni sobrescribe la ruta de la app viva

  Scenario: Una app que falla bajo portless no deja ruta, y es el espejo correcto
    Given una app que falla al arrancar bajo portless
    And portless imprime una url para ella
    Then portless NO registra ninguna ruta para esa app
    And vroom aplica el mismo criterio: url impresa sin ruta verificada no se publica

  # ==================================================================
  # 7. Reconciliacion: vroom es lo unico que puede limpiar sus rutas
  # ==================================================================

  Scenario: Reconciliacion retira una ruta cuyo nombre ya no corresponde
    Given un servicio que en un arranque anterior registro la ruta con el nombre N1
    And en este arranque el nombre derivado es N2, distinto de N1
    And la ruta N1 ya no responde porque su puerto esta muerto
    When el usuario arranca el servicio
    Then la reconciliacion retira la ruta N1
    And la ruta N2 queda registrada y verificada
    And no queda una ruta N1 apuntando a un puerto muerto

  Scenario: Reconciliacion retira las rutas que dejo un vroom que murio
    Given un servicio cuyo estado persistido declara una ruta registrada
    And esa ruta ya no responde porque el vroom que la creo murio sin pararla
    And el puerto de destino ya no lo escucha nadie
    When el usuario arranca el servicio con vroom
    Then vroom retira esa ruta huerfana
    And registra y verifica la ruta correcta para el puerto actual
    And nada depende de que prune haga o deje de hacer

  Scenario: Reconciliacion NO toca una ruta viva que no es de este servicio
    Given un servicio que en un arranque anterior registro la ruta N1
    And la ruta N1 responde porque otra cosa la ocupa de verdad
    When el usuario arranca el servicio
    Then vroom NO retira esa ruta
    And vroom avisa del conflicto en vez de romper algo ajeno
    And el fallo cerrado se aplica tambien a la limpieza

  Scenario: La reconciliacion es idempotente
    Given un servicio cuya ruta persistida ya coincide con la derivada
    And esa ruta responde
    When el usuario arranca el servicio varias veces seguidas
    Then la reconciliacion no retira nada en ninguna occasion
    And no se acumulan rutas duplicadas

  Scenario: Arrancar de nuevo sin cambios es idempotente
    Given un servicio en modo dynamic con ruta ya registrada en su puerto
    When el usuario arranca el servicio otra vez
    Then la ruta sigue registrada en ese mismo puerto
    And no se acumulan rutas duplicadas

  Scenario: La app reinicia y hace bind en un puerto distinto
    Given un servicio en modo dynamic con ruta registrada en el puerto P1
    And la app reinicia y hace bind en un puerto P2 distinto de P1
    When el usuario arranca el servicio de nuevo
    Then la misma ruta pasa a apuntar a P2
    And la ruta no queda apuntando a P1, que ya esta muerto
    And no queda mas de una ruta para el mismo servicio

  Scenario: Una ruta registrada con el proxy caido se sirve cuando el proxy vuelve
    Given un servicio en modo dynamic con puerto
    And la ruta quedo registrada mientras el proxy no corria
    When el proxy vuelve a estar en marcha
    Then la ruta que vroom registro sigue en el fichero de estado del proxy
    And el proxy la sirve sin que vroom tenga que registrarla otra vez

  # ==================================================================
  # 8. El ciclo de vida del stop
  # ==================================================================

  Scenario: Parar el servicio retira su ruta
    Given un servicio en modo dynamic con puerto y ruta registrada
    When el usuario para el servicio
    Then la ruta de ese servicio deja de existir en portless
    And la ruta no apunta a un puerto que ya no existe
    And las rutas de los servicios hermanos no se ven afectadas

  Scenario: Parar dos veces no es un error
    Given un servicio en modo dynamic que ya fue parado
    And su ruta ya no existe en portless
    When el usuario para el servicio otra vez
    Then el segundo stop TIENE EXITO
    And el fallo de quitar una ruta inexistente no se reporta como error
    And no se emite un error por un codigo de salida no cero del removedor

  # ==================================================================
  # 9. Compatibilidad hacia atras: el slice no molesta a nadie
  # ==================================================================

  Scenario: Un manifiesto sin route_mode se comporta exactamente como antes
    Given un manifiesto que no declara route_mode
    When vroom lo parsea
    Then el modo de ruta es off
    And el manifiesto es valido
    And vroom no busca el binario de portless
    And no se invoca a ningun seam de portless

  Scenario: route_mode exige un puerto en algun modo
    Given un manifiesto con route_mode distinto de off
    And el manifiesto no declara puerto en ningun modo
    Then el manifiesto es RECHAZADO con un error que lo dice

  Scenario: route_name sin route_mode named es un error
    Given un manifiesto que declara route_name
    And route_mode no es named
    Then el manifiesto es RECHAZADO con un error que lo dice

  Scenario: La CLI y la TUI degradan de forma identica
    Given cualquier resultado de ruta, incluido degraded
    When el usuario usa la CLI, la TUI, el refresco de estado y el stop
    Then ninguno de esos caminos falla
    And el JSON de la CLI conserva su forma y sus campos
    And el stop sigue funcionando con normalidad
    And el aviso es visible pero no bloqueante

  # ==================================================================
  # 10. La superficie que leen los agentes nunca miente
  # ==================================================================

  Scenario: El JSON distingue intencion de resultado
    Given un manifiesto que declara route_mode named
    And la ruta no se pudo registrar
    When el usuario consulta el JSON
    Then el modo de ruta publicado refleja la INTENCION del manifiesto
    And el objeto de ruta refleja el RESULTADO
    And el nombre publicado es el nombre PRETENDIDO, no una url

  Scenario: Una ruta no verificada no publica url
    Given cualquier servicio cuyo resultado de ruta sea degraded
    When el usuario consulta el JSON
    Then el campo url esta AUSENTE
    And hay un motivo legible por maquina presente
    And ningun campo afirma que exista una ruta disponible

  Scenario: Un servicio sin contrato de ruta no publica objeto de ruta
    Given un manifiesto sin route_mode
    When el usuario consulta el JSON
    Then el objeto de ruta esta AUSENTE
    And el JSON no afirma ni niega nada sobre rutas

  Scenario: El JSON no afirma que una ruta exista solo porque este escrita
    Given una ruta presente en el fichero de estado de portless
    And el proxy no la esta sirviendo
    When el usuario consulta el JSON
    Then el JSON no afirma que haya una ruta disponible
    And el motivo publicado dice que el proxy no responde