// Package protocol holds generated Wayland protocol bindings used by the
// wlr HUD backend.
//
// To regenerate after updating the source XML files:
//
//	go run codeberg.org/tesselslate/wl/cmd/scanner \
//	  -namespace zwlr \
//	  -out internal/hud/wlr/protocol/layer_shell_v1.go \
//	  internal/hud/wlr/protocol/layer_shell_v1.xml
//
// After regenerating, change the package declaration on line 24 from
// "package zwlr" to "package protocol".
//
// The .xml source files in this directory are the canonical inputs. Do not
// edit the generated .go files by hand (beyond the package line above).
package protocol
