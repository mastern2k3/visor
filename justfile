# visor — build recipes. `just` with no args lists them.

bindir := env("HOME") / "bin"

# go from PATH, else from the dev shell
go := if `command -v go > /dev/null 2>&1 && echo y || echo n` == "y" { "go" } else { "nix develop -c go" }

default:
    @just --list

# Build the static binary into bin/visor.
build:
    # CGO_ENABLED=0 is load-bearing on NixOS: a dynamically-linked binary names
    # an absolute /nix/store glibc path that a later GC removes, and the binary
    # then dies with "cannot execute: required file not found".
    cp scripts/visor-hook.sh cmd/visor/install_hook.sh
    CGO_ENABLED=0 {{go}} build -o bin/visor ./cmd/visor

# Run the test suite.
test:
    {{go}} test ./...

# Install the binary where contrib/systemd/ expects it.
install: build
    mkdir -p {{bindir}}
    install -m755 bin/visor {{bindir}}/visor
    @echo "installed {{bindir}}/visor"

# Remove build output.
clean:
    rm -rf bin
