{
  description = "visor — cross-WM attention HUD for Claude Code sessions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            # Go toolchain + LSP / format / debug
            go
            gopls
            gotools         # goimports, gorename, etc.
            gofumpt
            delve

            # Backend runtime / debugging
            eww             # default HUD backend
            wayland-utils   # wayland-info for wlr backend debugging
            wlr-randr       # output enumeration on wlr compositors
            xprop           # inspect EWMH state for the x11 backend / focus path
            xwininfo
            tmux            # focus dispatch target

            # General
            jq              # `visor ctl json` inspection
          ];

          shellHook = ''
            export GOPATH="$PWD/.go"
            export GOBIN="$GOPATH/bin"
            export PATH="$GOBIN:$PATH"
            echo "visor dev shell — go $(go version | awk '{print $3}')"
          '';
        };

        formatter = pkgs.nixpkgs-fmt;
      });
}
