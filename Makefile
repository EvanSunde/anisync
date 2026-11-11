PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

anisync: $(shell find . -name '*.go') go.mod
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o anisync ./cmd/anisync

test:
	go test ./...

# needs dbus-daemon; runs the real daemon against a fake MPRIS player and mock APIs
e2e:
	dbus-run-session -- go test -tags e2e ./internal/e2e/ -count=1

install: anisync
	install -Dm755 anisync $(PREFIX)/bin/anisync
	install -Dm644 contrib/anisync.service $(HOME)/.config/systemd/user/anisync.service
	@$(PREFIX)/bin/anisync init
	@echo
	@echo "Next: edit ~/.config/anisync/config.toml, run 'anisync auth anilist' / 'anisync auth mal',"
	@echo "then: systemctl --user daemon-reload && systemctl --user enable --now anisync"

uninstall:
	-systemctl --user disable --now anisync
	rm -f $(PREFIX)/bin/anisync $(HOME)/.config/systemd/user/anisync.service

clean:
	rm -f anisync

.PHONY: test e2e install uninstall clean
