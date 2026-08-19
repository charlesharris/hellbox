SHELL := /usr/bin/env bash
# Go is installed under the user's home rather than system-wide, and sudo's
# secure_path hides it. Fall back to the invoking user's home, not root's, so
# `sudo make install` can still find the toolchain.
GO_HOME ?= $(if $(SUDO_USER),$(shell getent passwd $(SUDO_USER) | cut -d: -f6),$(HOME))
GO      ?= $(shell command -v go 2>/dev/null || echo $(GO_HOME)/.local/go/bin/go)

PREFIX      ?= /usr/local
CONFIG_DIR  ?= /etc/hellbox
STATE_DIR   ?= /var/lib/hellbox
RUN_DIR     ?= /run/hellbox
# The account hellboxd will run as, and which therefore has to own the state
# directory and the socket directory.
#
# Deliberately not named USER: that is already an environment variable, `?=`
# does not override one, and sudo sets it to root — so a USER ?= assignment
# silently evaluates to root under exactly the command this needs to get right,
# leaving the daemon unable to write its own database. SUDO_USER names the
# invoking user and is only set when sudo is in play.
RUN_USER    ?= $(if $(SUDO_USER),$(SUDO_USER),$(shell id -un))
RUN_GROUP   ?= $(shell id -gn $(RUN_USER))

# The service unit needs that user's home: MakeMKV's registration key lives
# there, and the unit grants write access to that one directory.
RUN_HOME    ?= $(shell getent passwd $(RUN_USER) | cut -d: -f6)

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build test vet fmt check clean install install-service uninstall

all: build

build:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/hellboxd ./cmd/hellboxd
	$(GO) build -trimpath -ldflags "-s -w" -o bin/slay ./cmd/slay

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Run the startup checks against the real hardware without starting the daemon.
check: build
	./bin/hellboxd -check

clean:
	rm -rf bin

# STATE_DIR holds the database. RUN_DIR holds the socket and lives on tmpfs:
# systemd recreates it per boot via RuntimeDirectory=, but `hellboxd -check` run
# by hand needs it too, and an ordinary user cannot create it under /run.
install: build
	install -d $(PREFIX)/bin $(CONFIG_DIR)
	install -m 0755 bin/hellboxd $(PREFIX)/bin/hellboxd
	install -m 0755 bin/slay $(PREFIX)/bin/slay
	@if [ ! -f $(CONFIG_DIR)/config.toml ]; then \
		install -m 0644 config.example.toml $(CONFIG_DIR)/config.toml; \
		echo "installed $(CONFIG_DIR)/config.toml"; \
	else \
		echo "kept existing $(CONFIG_DIR)/config.toml"; \
	fi
	install -d -o $(RUN_USER) -g $(RUN_GROUP) $(STATE_DIR)
	install -d -o $(RUN_USER) -g $(RUN_GROUP) -m 0750 $(RUN_DIR)

# Installs the systemd unit. hellboxd runs as a normal user in the cdrom group
# rather than as root: it needs the optical device and the media directories,
# and nothing else.
install-service: install
	sed -e 's|@PREFIX@|$(PREFIX)|g' \
	    -e 's|@USER@|$(RUN_USER)|g' \
	    -e 's|@GROUP@|$(RUN_GROUP)|g' \
	    -e 's|@HOME@|$(RUN_HOME)|g' \
	    -e 's|@CONFIG_DIR@|$(CONFIG_DIR)|g' \
	    packaging/hellboxd.service.in > /etc/systemd/system/hellboxd.service
	systemctl daemon-reload
	@echo
	@echo "Now run:  systemctl enable --now hellboxd"

uninstall:
	-systemctl disable --now hellboxd
	rm -f /etc/systemd/system/hellboxd.service $(PREFIX)/bin/hellboxd $(PREFIX)/bin/slay
	systemctl daemon-reload
	@echo "Left $(CONFIG_DIR) and $(STATE_DIR) in place; remove them by hand if wanted."
