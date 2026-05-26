package wm

import (
	"encoding/json"
	"strconv"
	"strings"
)

// niriFocusedID returns the niri window id (numeric) of the focused window.
// niri msg --json focused-window  →  {"id": N, ...}
func niriFocusedID() string {
	out := runOut("niri", "msg", "--json", "focused-window")
	if out == "" {
		return ""
	}
	var v struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return ""
	}
	return v.ID.String()
}

// swayFocusedID returns the focused window's con_id.
// swaymsg -t get_tree | jq … — we walk the JSON ourselves to avoid jq dep.
func swayFocusedID() string {
	out := runOut("swaymsg", "-t", "get_tree")
	if out == "" {
		return ""
	}
	return walkSwayTreeFocused([]byte(out))
}

func walkSwayTreeFocused(b []byte) string {
	var node map[string]any
	if err := json.Unmarshal(b, &node); err != nil {
		return ""
	}
	if f, _ := node["focused"].(bool); f {
		if id, ok := node["id"].(json.Number); ok {
			return id.String()
		}
		// json.Unmarshal into any uses float64 for numbers — handle that.
		if id, ok := node["id"].(float64); ok {
			return strconv.FormatFloat(id, 'f', -1, 64)
		}
	}
	for _, key := range []string{"nodes", "floating_nodes"} {
		if children, ok := node[key].([]any); ok {
			for _, c := range children {
				cb, _ := json.Marshal(c)
				if id := walkSwayTreeFocused(cb); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// hyprFocusedID returns the active window address from hyprctl.
func hyprFocusedID() string {
	out := runOut("hyprctl", "-j", "activewindow")
	if out == "" {
		return ""
	}
	var v struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return ""
	}
	return v.Address
}

// x11FocusedID returns the X11 window id (hex) of the focused window via xdotool.
func x11FocusedID() string {
	return strings.TrimSpace(runOut("xdotool", "getactivewindow"))
}
