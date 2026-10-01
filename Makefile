.PHONY: build install test lint check run clean mutate mutate-diff

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"
INSTALL_DIR := $(HOME)/.local/bin
GOLANGCI_LINT_VERSION := v2.13.2
MUTATE_BASE ?= main

# El sello de procedencia se exige, no se supone. Medido en este repo:
#   - compilando desde el checkout principal, Go estampa vcs.revision con el
#     default (auto), sin necesidad de -buildvcs;
#   - compilando desde un worktree ENLAZADO, Go NO estampa nada y no falla: el
#     binario sale sin procedencia y sin error.
# Un binario sin sello es indistinguible de uno viejo, y quien ejecuta
# ~/.local/bin/vroom no tiene forma de saber que revision esta probando.
build:
	go build -buildvcs=true $(LDFLAGS) -o .local/bin/vroom ./cmd/vroom

# El binario desplegado se verifica contra el HEAD desde el que se compilo. No
# se exige compilar desde el checkout principal: un binario con el sello del
# commit que se acaba de compilar es correcto dondequiera que se compilo. Lo que
# no es admisible es un binario sin sello, o con un sello que no sea el de esta
# compilacion. Asi fue como se desplego una revision vieja sin que nadie lo
# notara: el deploy corrio contra un arbol que aun no era el que se iba a
# mergear, y no habia nada que lo dijera.
install: build
	@mv -f .local/bin/vroom $(INSTALL_DIR)/vroom.tmp && mv -f $(INSTALL_DIR)/vroom.tmp $(INSTALL_DIR)/vroom
	@want=$$(git rev-parse HEAD); \
	 got=$$(go version -m $(INSTALL_DIR)/vroom 2>/dev/null | sed -n 's/.*vcs\.revision=//p'); \
	 if [ "$$got" != "$$want" ]; then \
	   echo "install: ABORTADO — el binario no lleva la revision recien compilada." >&2; \
	   echo "  revision compilada: $$want" >&2; \
	   echo "  revision estampada: $${got:-<ninguna: el binario no fue compilado con -buildvcs=true>}" >&2; \
	   echo "  Sin sello, esto es indistinguible de un binario viejo. No se deploya." >&2; \
	   exit 1; \
	 fi; \
	 mod=$$(go version -m $(INSTALL_DIR)/vroom 2>/dev/null | sed -n 's/.*vcs\.modified=//p'); \
	 echo "install: $(INSTALL_DIR)/vroom -> $$want$${mod:+ (arbol modificado: $$mod)}"

run:
	go run ./cmd/vroom

test:
	go test -race -count=1 -cover ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

check: build lint test

mutate:
	go tool gremlins unleash --workers 4 --timeout-coefficient 3 --output report.json

# gremlins silently falls back to the whole module when the diff is empty (base == HEAD),
# so fail fast instead of running a full-module run that looks diff-scoped.
mutate-diff:
	@if git diff --name-only $(MUTATE_BASE)...HEAD | grep -q '\.go$$'; then \
		go tool gremlins unleash --diff $(MUTATE_BASE) --workers 4 --timeout-coefficient 3 --output report.json; \
	else \
		echo "no .go changes vs $(MUTATE_BASE) - nothing to mutate"; \
	fi

clean:
	rm -rf .local/bin/
