.PHONY: build test sqlc install rebuild uninstall status clean help

BIN := ./antitimely

help:
	@echo "Antitimely Makefile targets:"
	@echo "  make build      build the binary at $(BIN)"
	@echo "  make test       run go test ./... -count=1"
	@echo "  make sqlc       regenerate internal/store from schema.sql/queries.sql"
	@echo "  make install    build + install launch agent (first-time setup)"
	@echo "  make rebuild    build + restart the running launch agent"
	@echo "  make uninstall  stop and remove the launch agent"
	@echo "  make status     show the daemon's current status"
	@echo "  make clean      remove the local binary"

build:
	go build -o $(BIN) .

test:
	go test ./... -count=1

sqlc:
	sqlc generate

install: build
	$(BIN) install-launch-agent

rebuild: build
	@if $(BIN) status > /dev/null 2>&1; then \
		echo "Daemon is running — restarting via launchctl…"; \
		$(BIN) uninstall-launch-agent && $(BIN) install-launch-agent; \
	else \
		echo "Daemon not running — installing fresh…"; \
		$(BIN) install-launch-agent; \
	fi
	@sleep 1
	@$(BIN) status | head -3

uninstall:
	$(BIN) uninstall-launch-agent

status:
	$(BIN) status

clean:
	rm -f $(BIN)
