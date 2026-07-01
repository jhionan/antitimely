.PHONY: build test sqlc install rebuild uninstall status clean help

BIN := ./antitimely

# Code-signing identity for the built binary. Go's linker emits an ad-hoc
# signature whose code hash (cdhash) changes on every build, which makes macOS
# TCC drop the Accessibility grant each rebuild. Signing with a STABLE identity
# keys the grant to the Team ID instead of the hash, so it persists across
# rebuilds. Override with `make build SIGN_ID="..."`, or SIGN_ID= to skip.
SIGN_ID ?= Developer ID Application: Jhionan Rian Lara dos Santos (5KNATBVY62)
BUNDLE_ID := com.rian.antitimely

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
	@if [ -n "$(SIGN_ID)" ]; then \
		if codesign --force --sign "$(SIGN_ID)" --identifier "$(BUNDLE_ID)" $(BIN) 2>/dev/null; then \
			echo "signed with stable identity — Accessibility grant persists across rebuilds"; \
		else \
			echo "warning: codesign with '$(SIGN_ID)' failed; kept Go's ad-hoc signature (Accessibility grant will reset on rebuild)"; \
		fi; \
	fi

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
