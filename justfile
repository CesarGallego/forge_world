set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

app := "forgeworld"
cmd := "./cmd/forgeworld"
build_bin := "./{{app}}"

default: build

build:
	go build -o "{{build_bin}}" "{{cmd}}"

install: build
	install -d "$HOME/.local/bin"
	install -m 0755 "{{build_bin}}" "$HOME/.local/bin/{{app}}"
	install -d "$HOME/.config/forgeworld"
	install -m 0644 templates/prompts/*.md "$HOME/.config/forgeworld/"

uninstall:
	rm -f "$HOME/.local/bin/{{app}}"

clean:
	rm -f "{{build_bin}}"
