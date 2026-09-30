# Lenguaje: Gherkin. Comportamiento esperado, no steps de Cucumber.
#
# SLICE 4 — portless como proxy PURO. DEFERRIDO A LO ULTIMO.
#
# Regla que gobierna todo este archivo: vroom NUNCA asume que el usuario ya
# actualizo su node global. El slice 4 se escribe como un PR independiente y su
# ausencia NO bloquea a los slices 1-3, que se liberan sin el.
#
# Precondicion del entorno (medida): portless v0.13.0 esta instalado pero hoy NO
# resuelve por el shim de mise (default global node 20.19.0). Por eso
# "portless no disponible" es un estado NORMAL y NO FATAL en todos los caminos.

Feature: portless como proxy puro, con degradacion limpia cuando no esta

  Scenario: Arranque dinamico sin portless instalado en el sistema
    Given el usuario no tiene un binario "portless" resoluble en el PATH
    When el usuario arranca un servicio en modo dinamico
    Then el servicio arranca normalmente
    And el puerto se reserva, se inyecta y se descubre igual que sin portless
    And vroom emite un aviso claro de que la capa de proxy no esta disponible
    And el comportamiento de vroom es identico al de hoy en todo lo demas

  Scenario: portless instalado pero el shim falla al resolver
    Given existe un shim "portless" en el PATH pero su ejecucion falla
    When el usuario arranca un servicio en modo dinamico
    Then vroom NO aborta el arranque
    And vroom reporta el fallo concreto del binario
    And el resto de la supervision sigue intacta

  Scenario: vroom no modifica la configuracion de node del usuario
    Given el usuario tiene su default global de node por debajo de 24
    When vroom resuelve portless
    Then vroom lo busca en el PATH y no fija ninguna version de node
    And vroom NO escribe ni cambia ningun default global del usuario
    And vroom NO condiciona el resto de sus funciones al resultado

  Scenario: La degradacion es identica en todos los caminos
    Given portless no esta disponible
    When el usuario usa la TUI, el JSON de la CLI, el refresco de estado y el stop
    Then ninguno de esos caminos falla
    And el JSON de la CLI mantiene su forma y sus campos
    And el stop sigue funcionando con normalidad
    And el aviso de indisponibilidad es visible pero no bloqueante

  Scenario: Con portless disponible, vroom sigue siendo el dueno del puerto
    Given portless esta disponible
    And un servicio en modo dinamico arranca
    Then vroom elige el puerto y se lo entrega a portless
    And portless no decide ni reasigna el puerto del servicio
    And el ciclo de vida del proceso de la app lo gestiona vroom, no portless

  Scenario: Nombre de ruta estable y distinto por worktree
    Given dos worktrees del mismo repo con servicios en modo dinamico
    And portless esta disponible
    When ambos servicios arrancan
    Then cada uno tiene su propia ruta con nombre distinto
    And la ruta de cada uno se deriva de la topologia de worktree que vroom ya
      conoce, sin recalcularla
    And la ruta de cada uno apunta al puerto real de ese servicio

  Scenario: Detener un servicio no deja el proxy apuntando a un puerto muerto
    Given un servicio en modo dinamico detenido
    When vroom reconcilia el estado del proxy
    Then la ruta de ese servicio deja de apuntar al puerto que ya no existe
    And los servicios vivos no se ven afectados

  Scenario: El proxy no es un servicio mas de vroom por defecto
    Given portless esta disponible
    When el usuario ve la lista de servicios
    Then el proxy no aparece mezclado con los servicios del proyecto
    And el usuario no puede detener el proxy por la via normal de un servicio