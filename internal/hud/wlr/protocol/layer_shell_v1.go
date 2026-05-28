// Copyright © 2017 Drew DeVault
//
// Permission to use, copy, modify, distribute, and sell this
// software and its documentation for any purpose is hereby granted
// without fee, provided that the above copyright notice appear in
// all copies and that both that copyright notice and this permission
// notice appear in supporting documentation, and that the name of
// the copyright holders not be used in advertising or publicity
// pertaining to distribution of the software without specific,
// written prior permission.  The copyright holders make no
// representations about the suitability of this software for any
// purpose.  It is provided "as is" without express or implied
// warranty.
//
// THE COPYRIGHT HOLDERS DISCLAIM ALL WARRANTIES WITH REGARD TO THIS
// SOFTWARE, INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND
// FITNESS, IN NO EVENT SHALL THE COPYRIGHT HOLDERS BE LIABLE FOR ANY
// SPECIAL, INDIRECT OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN
// AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION,
// ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF
// THIS SOFTWARE.

package protocol

import (
	"fmt"

	"codeberg.org/tesselslate/wl"
	"codeberg.org/tesselslate/wl-protocols/xdg"
)

// # Create surfaces that are layers of the desktop
//
// Clients can use this interface to assign the surface_layer role to
// wl_surfaces. Such surfaces are assigned to a "layer" of the output and
// rendered with a defined z-depth respective to each other. They may also be
// anchored to the edges and corners of a screen and specify input handling
// semantics. This interface should be suitable for the implementation of
// many desktop shell components, and a broad number of other applications
// that interact with the desktop.
type LayerShellV1 wl.Object

// Note: Do not modify this variable.
var LayerShellV1Interface = wl.Interface{
	ErrorStr: errorStrLayerShellV1,
	Dispatch: nil,
	NumFd:    nil,
	Name:     "zwlr_layer_shell_v1",
}

func errorStrLayerShellV1(code uint32) string {
	return LayerShellV1Error(code).String()
}

type LayerShellV1Error int32

const (
	LayerShellV1ErrorRole               LayerShellV1Error = 0 // wl_surface has another role
	LayerShellV1ErrorInvalidLayer       LayerShellV1Error = 1 // Layer value is invalid
	LayerShellV1ErrorAlreadyConstructed LayerShellV1Error = 2 // wl_surface has a buffer attached or committed
)

const strLayerShellV1Error = "roleinvalid_layeralready_constructed"

var idxLayerShellV1Error = [...]uint8{0, 4, 17, 36}

func (v LayerShellV1Error) String() string {
	if v < 0 || v >= LayerShellV1Error(len(idxLayerShellV1Error)-1) {
		return fmt.Sprintf("LayerShellV1Error(%d)", v)
	}
	return strLayerShellV1Error[idxLayerShellV1Error[v]:idxLayerShellV1Error[v+1]]
}

// # Available layers for surfaces
//
// These values indicate which layers a surface can be rendered in. They
// are ordered by z depth, bottom-most first. Traditional shell surfaces
// will typically be rendered between the bottom and top layers.
// Fullscreen shell surfaces are typically rendered at the top layer.
// Multiple surfaces can share a single layer, and ordering within a
// single layer is undefined.
type LayerShellV1Layer int32

const (
	LayerShellV1LayerBackground LayerShellV1Layer = 0
	LayerShellV1LayerBottom     LayerShellV1Layer = 1
	LayerShellV1LayerTop        LayerShellV1Layer = 2
	LayerShellV1LayerOverlay    LayerShellV1Layer = 3
)

const strLayerShellV1Layer = "backgroundbottomtopoverlay"

var idxLayerShellV1Layer = [...]uint8{0, 10, 16, 19, 26}

func (v LayerShellV1Layer) String() string {
	if v < 0 || v >= LayerShellV1Layer(len(idxLayerShellV1Layer)-1) {
		return fmt.Sprintf("LayerShellV1Layer(%d)", v)
	}
	return strLayerShellV1Layer[idxLayerShellV1Layer[v]:idxLayerShellV1Layer[v+1]]
}

// # Create a layer_surface from a surface
//
// Create a layer surface for an existing surface. This assigns the role of
// layer_surface, or raises a protocol error if another role is already
// assigned.
//
// Creating a layer surface from a wl_surface which has a buffer attached
// or committed is a client error, and any attempts by a client to attach
// or manipulate a buffer prior to the first layer_surface.configure call
// must also be treated as errors.
//
// After creating a layer_surface object and setting it up, the client
// must perform an initial commit without any buffer attached.
// The compositor will reply with a layer_surface.configure event.
// The client must acknowledge it and is then allowed to attach a buffer
// to map the surface.
//
// You may pass NULL for output to allow the compositor to decide which
// output to use. Generally this will be the one that the user most
// recently interacted with.
//
// Clients can specify a namespace that defines the purpose of the layer
// surface.
func (S *LayerShellV1) GetLayerSurface(surface wl.Surface, output wl.Output, layer LayerShellV1Layer, namespace string) LayerSurfaceV1 {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	R := M.WriteNewIdStatic(*O, &LayerSurfaceV1Interface)
	M.WriteObject(wl.Object(surface), false)
	M.WriteObject(wl.Object(output), true)
	M.WriteUint(uint32(layer))
	M.WriteString(namespace, false)
	M.WriteHeader(O.GetId(), 0)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "get_layer_surface", wl.NewId(R), wl.Object(surface), wl.Object(output), uint32(layer), namespace)
	}
	return LayerSurfaceV1(R)
}

// # Destroy the layer_shell object
//
// This request indicates that the client will not use the layer_shell
// object any more. Objects that have been created through this instance
// are not affected.
func (S *LayerShellV1) Destroy() {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteHeader(O.GetId(), 1)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "destroy")
	}
	O.Destroy()
}

// # Layer metadata interface
//
// An interface that may be implemented by a wl_surface, for surfaces that
// are designed to be rendered as a layer of a stacked desktop-like
// environment.
//
// Layer surface state (layer, size, anchor, exclusive zone,
// margin, interactivity) is double-buffered, and will be applied at the
// time wl_surface.commit of the corresponding wl_surface is called.
//
// Attaching a null buffer to a layer surface unmaps it.
//
// Unmapping a layer_surface means that the surface cannot be shown by the
// compositor until it is explicitly mapped again. The layer_surface
// returns to the state it had right after layer_shell.get_layer_surface.
// The client can re-map the surface by performing a commit without any
// buffer attached, waiting for a configure event and handling it as usual.
type LayerSurfaceV1 wl.Object

type LayerSurfaceV1Listener struct {
	// # Suggest a surface change
	//
	// The configure event asks the client to resize its surface.
	//
	// Clients should arrange their surface for the new states, and then send
	// an ack_configure request with the serial sent in this configure event at
	// some point before committing the new surface.
	//
	// The client is free to dismiss all but the last configure event it
	// received.
	//
	// The width and height arguments specify the size of the window in
	// surface-local coordinates.
	//
	// The size is a hint, in the sense that the client is free to ignore it if
	// it doesn't resize, pick a smaller size (to satisfy aspect ratio or
	// resize in steps of NxM pixels). If the client picks a smaller size and
	// is anchored to two opposite anchors (e.g. 'top' and 'bottom'), the
	// surface will be centered on this axis.
	//
	// If the width or height arguments are zero, it means the client should
	// decide its own window dimension.
	Configure func(data any, self LayerSurfaceV1, serial uint32, width uint32, height uint32) error

	// # Surface should be closed
	//
	// The closed event is sent by the compositor when the surface will no
	// longer be shown. The output may have been destroyed or the user may
	// have asked for it to be removed. Further changes to the surface will be
	// ignored. The client should destroy the resource after receiving this
	// event, and create a new surface if they so choose.
	Closed func(data any, self LayerSurfaceV1) error

	// Unexported. Forbids unkeyed struct initialization.
	_ struct{}
}

// Note: Do not modify this variable.
var LayerSurfaceV1Interface = wl.Interface{
	ErrorStr: errorStrLayerSurfaceV1,
	Dispatch: []func(wl.Object, wl.Message) error{dispatchLayerSurfaceV1Configure, dispatchLayerSurfaceV1Closed},
	NumFd:    []int{0, 0},
	Name:     "zwlr_layer_surface_v1",
}

func errorStrLayerSurfaceV1(code uint32) string {
	return LayerSurfaceV1Error(code).String()
}

// SetListener sets the event listener for the LayerSurfaceV1. Overwriting an existing
// listener is illegal and will result in a panic.
func (o *LayerSurfaceV1) SetListener(listener LayerSurfaceV1Listener, data any) {
	(*wl.Object)(o).SetListener(listener, data)
}

// # Types of keyboard interaction possible for a layer shell surface
//
// Types of keyboard interaction possible for layer shell surfaces. The
// rationale for this is twofold: (1) some applications are not interested
// in keyboard events and not allowing them to be focused can improve the
// desktop experience; (2) some applications will want to take exclusive
// keyboard focus.
type LayerSurfaceV1KeyboardInteractivity int32

const (
	// # No keyboard focus is possible
	//
	// This value indicates that this surface is not interested in keyboard
	// events and the compositor should never assign it the keyboard focus.
	//
	// This is the default value, set for newly created layer shell surfaces.
	//
	// This is useful for e.g. desktop widgets that display information or
	// only have interaction with non-keyboard input devices.
	LayerSurfaceV1KeyboardInteractivityNone LayerSurfaceV1KeyboardInteractivity = 0
	// # Request exclusive keyboard focus
	//
	// Request exclusive keyboard focus if this surface is above the shell surface layer.
	//
	// For the top and overlay layers, the seat will always give
	// exclusive keyboard focus to the top-most layer which has keyboard
	// interactivity set to exclusive. If this layer contains multiple
	// surfaces with keyboard interactivity set to exclusive, the compositor
	// determines the one receiving keyboard events in an implementation-
	// defined manner. In this case, no guarantee is made when this surface
	// will receive keyboard focus (if ever).
	//
	// For the bottom and background layers, the compositor is allowed to use
	// normal focus semantics.
	//
	// This setting is mainly intended for applications that need to ensure
	// they receive all keyboard events, such as a lock screen or a password
	// prompt.
	LayerSurfaceV1KeyboardInteractivityExclusive LayerSurfaceV1KeyboardInteractivity = 1
	// # Request regular keyboard focus semantics
	//
	// This requests the compositor to allow this surface to be focused and
	// unfocused by the user in an implementation-defined manner. The user
	// should be able to unfocus this surface even regardless of the layer
	// it is on.
	//
	// Typically, the compositor will want to use its normal mechanism to
	// manage keyboard focus between layer shell surfaces with this setting
	// and regular toplevels on the desktop layer (e.g. click to focus).
	// Nevertheless, it is possible for a compositor to require a special
	// interaction to focus or unfocus layer shell surfaces (e.g. requiring
	// a click even if focus follows the mouse normally, or providing a
	// keybinding to switch focus between layers).
	//
	// This setting is mainly intended for desktop shell components (e.g.
	// panels) that allow keyboard interaction. Using this option can allow
	// implementing a desktop shell that can be fully usable without the
	// mouse.
	LayerSurfaceV1KeyboardInteractivityOnDemand LayerSurfaceV1KeyboardInteractivity = 2
)

const strLayerSurfaceV1KeyboardInteractivity = "noneexclusiveon_demand"

var idxLayerSurfaceV1KeyboardInteractivity = [...]uint8{0, 4, 13, 22}

func (v LayerSurfaceV1KeyboardInteractivity) String() string {
	if v < 0 || v >= LayerSurfaceV1KeyboardInteractivity(len(idxLayerSurfaceV1KeyboardInteractivity)-1) {
		return fmt.Sprintf("LayerSurfaceV1KeyboardInteractivity(%d)", v)
	}
	return strLayerSurfaceV1KeyboardInteractivity[idxLayerSurfaceV1KeyboardInteractivity[v]:idxLayerSurfaceV1KeyboardInteractivity[v+1]]
}

type LayerSurfaceV1Error int32

const (
	LayerSurfaceV1ErrorInvalidSurfaceState          LayerSurfaceV1Error = 0 // Provided surface state is invalid
	LayerSurfaceV1ErrorInvalidSize                  LayerSurfaceV1Error = 1 // Size is invalid
	LayerSurfaceV1ErrorInvalidAnchor                LayerSurfaceV1Error = 2 // Anchor bitfield is invalid
	LayerSurfaceV1ErrorInvalidKeyboardInteractivity LayerSurfaceV1Error = 3 // Keyboard interactivity is invalid
	LayerSurfaceV1ErrorInvalidExclusiveEdge         LayerSurfaceV1Error = 4 // Exclusive edge is invalid given the surface anchors
)

const strLayerSurfaceV1Error = "invalid_surface_stateinvalid_sizeinvalid_anchorinvalid_keyboard_interactivityinvalid_exclusive_edge"

var idxLayerSurfaceV1Error = [...]uint8{0, 21, 33, 47, 77, 99}

func (v LayerSurfaceV1Error) String() string {
	if v < 0 || v >= LayerSurfaceV1Error(len(idxLayerSurfaceV1Error)-1) {
		return fmt.Sprintf("LayerSurfaceV1Error(%d)", v)
	}
	return strLayerSurfaceV1Error[idxLayerSurfaceV1Error[v]:idxLayerSurfaceV1Error[v+1]]
}

type LayerSurfaceV1Anchor uint32

const (
	LayerSurfaceV1AnchorTop    LayerSurfaceV1Anchor = 1 // The top edge of the anchor rectangle
	LayerSurfaceV1AnchorBottom LayerSurfaceV1Anchor = 2 // The bottom edge of the anchor rectangle
	LayerSurfaceV1AnchorLeft   LayerSurfaceV1Anchor = 4 // The left edge of the anchor rectangle
	LayerSurfaceV1AnchorRight  LayerSurfaceV1Anchor = 8 // The right edge of the anchor rectangle
)

func (v LayerSurfaceV1Anchor) String() string {
	return fmt.Sprintf("LayerSurfaceV1Anchor(%x)", uint32(v))
}

func dispatchLayerSurfaceV1Configure(O wl.Object, M wl.Message) error {
	serial, err := M.ReadUint()
	if err != nil {
		return err
	}
	width, err := M.ReadUint()
	if err != nil {
		return err
	}
	height, err := M.ReadUint()
	if err != nil {
		return err
	}

	L, K := O.GetListener().(LayerSurfaceV1Listener)
	if !K && O.Debug() {
		M.DebugEvent(O.GetDisplay(), true, "configure", serial, width, height)
		return nil
	}

	F := L.Configure
	if O.Debug() {
		M.DebugEvent(O.GetDisplay(), F == nil, "configure", serial, width, height)
	}

	var R error
	if F != nil {
		R = F(O.GetData(), LayerSurfaceV1(O), serial, width, height)
	}
	return R
}

func dispatchLayerSurfaceV1Closed(O wl.Object, M wl.Message) error {

	L, K := O.GetListener().(LayerSurfaceV1Listener)
	if !K && O.Debug() {
		M.DebugEvent(O.GetDisplay(), true, "closed")
		return nil
	}

	F := L.Closed
	if O.Debug() {
		M.DebugEvent(O.GetDisplay(), F == nil, "closed")
	}

	var R error
	if F != nil {
		R = F(O.GetData(), LayerSurfaceV1(O))
	}
	return R
}

// # Sets the size of the surface
//
// Sets the size of the surface in surface-local coordinates. The
// compositor will display the surface centered with respect to its
// anchors.
//
// If you pass 0 for either value, the compositor will assign it and
// inform you of the assignment in the configure event. You must set your
// anchor to opposite edges in the dimensions you omit; not doing so is a
// protocol error. Both values are 0 by default.
//
// Size is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetSize(width uint32, height uint32) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(width)
	M.WriteUint(height)
	M.WriteHeader(O.GetId(), 0)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_size", width, height)
	}
}

// # Configures the anchor point of the surface
//
// Requests that the compositor anchor the surface to the specified edges
// and corners. If two orthogonal edges are specified (e.g. 'top' and
// 'left'), then the anchor point will be the intersection of the edges
// (e.g. the top left corner of the output); otherwise the anchor point
// will be centered on that edge, or in the center if none is specified.
//
// Anchor is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetAnchor(anchor LayerSurfaceV1Anchor) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(uint32(anchor))
	M.WriteHeader(O.GetId(), 1)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_anchor", uint32(anchor))
	}
}

// # Configures the exclusive geometry of this surface
//
// Requests that the compositor avoids occluding an area with other
// surfaces. The compositor's use of this information is
// implementation-dependent - do not assume that this region will not
// actually be occluded.
//
// A positive value is only meaningful if the surface is anchored to one
// edge or an edge and both perpendicular edges. If the surface is not
// anchored, anchored to only two perpendicular edges (a corner), anchored
// to only two parallel edges or anchored to all edges, a positive value
// will be treated the same as zero.
//
// A positive zone is the distance from the edge in surface-local
// coordinates to consider exclusive.
//
// Surfaces that do not wish to have an exclusive zone may instead specify
// how they should interact with surfaces that do. If set to zero, the
// surface indicates that it would like to be moved to avoid occluding
// surfaces with a positive exclusive zone. If set to -1, the surface
// indicates that it would not like to be moved to accommodate for other
// surfaces, and the compositor should extend it all the way to the edges
// it is anchored to.
//
// For example, a panel might set its exclusive zone to 10, so that
// maximized shell surfaces are not shown on top of it. A notification
// might set its exclusive zone to 0, so that it is moved to avoid
// occluding the panel, but shell surfaces are shown underneath it. A
// wallpaper or lock screen might set their exclusive zone to -1, so that
// they stretch below or over the panel.
//
// The default value is 0.
//
// Exclusive zone is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetExclusiveZone(zone int32) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteInt(zone)
	M.WriteHeader(O.GetId(), 2)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_exclusive_zone", zone)
	}
}

// # Sets a margin from the anchor point
//
// Requests that the surface be placed some distance away from the anchor
// point on the output, in surface-local coordinates. Setting this value
// for edges you are not anchored to has no effect.
//
// The exclusive zone includes the margin.
//
// Margin is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetMargin(top int32, right int32, bottom int32, left int32) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteInt(top)
	M.WriteInt(right)
	M.WriteInt(bottom)
	M.WriteInt(left)
	M.WriteHeader(O.GetId(), 3)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_margin", top, right, bottom, left)
	}
}

// # Requests keyboard events
//
// Set how keyboard events are delivered to this surface. By default,
// layer shell surfaces do not receive keyboard events; this request can
// be used to change this.
//
// This setting is inherited by child surfaces set by the get_popup
// request.
//
// Layer surfaces receive pointer, touch, and tablet events normally. If
// you do not want to receive them, set the input region on your surface
// to an empty region.
//
// Keyboard interactivity is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetKeyboardInteractivity(keyboardInteractivity LayerSurfaceV1KeyboardInteractivity) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(uint32(keyboardInteractivity))
	M.WriteHeader(O.GetId(), 4)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_keyboard_interactivity", uint32(keyboardInteractivity))
	}
}

// # Assign this layer_surface as an xdg_popup parent
//
// This assigns an xdg_popup's parent to this layer_surface.  This popup
// should have been created via xdg_surface::get_popup with the parent set
// to NULL, and this request must be invoked before committing the popup's
// initial state.
//
// See the documentation of xdg_popup for more details about what an
// xdg_popup is and how it is used.
func (S *LayerSurfaceV1) GetPopup(popup xdg.Popup) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteObject(wl.Object(popup), false)
	M.WriteHeader(O.GetId(), 5)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "get_popup", wl.Object(popup))
	}
}

// # Ack a configure event
//
// When a configure event is received, if a client commits the
// surface in response to the configure event, then the client
// must make an ack_configure request sometime before the commit
// request, passing along the serial of the configure event.
//
// If the client receives multiple configure events before it
// can respond to one, it only has to ack the last configure event.
//
// A client is not required to commit immediately after sending
// an ack_configure request - it may even ack_configure several times
// before its next surface commit.
//
// A client may send multiple ack_configure requests before committing, but
// only the last request sent before a commit indicates which configure
// event the client really is responding to.
func (S *LayerSurfaceV1) AckConfigure(serial uint32) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(serial)
	M.WriteHeader(O.GetId(), 6)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "ack_configure", serial)
	}
}

// # Destroy the layer_surface
//
// This request destroys the layer surface.
func (S *LayerSurfaceV1) Destroy() {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteHeader(O.GetId(), 7)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "destroy")
	}
	O.Destroy()
}

// # Change the layer of the surface
//
// Change the layer that the surface is rendered on.
//
// Layer is double-buffered, see wl_surface.commit.
func (S *LayerSurfaceV1) SetLayer(layer LayerShellV1Layer) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(uint32(layer))
	M.WriteHeader(O.GetId(), 8)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_layer", uint32(layer))
	}
}

// # Set the edge the exclusive zone will be applied to
//
// Requests an edge for the exclusive zone to apply. The exclusive
// edge will be automatically deduced from anchor points when possible,
// but when the surface is anchored to a corner, it will be necessary
// to set it explicitly to disambiguate, as it is not possible to deduce
// which one of the two corner edges should be used.
//
// The edge must be one the surface is anchored to, otherwise the
// invalid_exclusive_edge protocol error will be raised.
func (S *LayerSurfaceV1) SetExclusiveEdge(edge LayerSurfaceV1Anchor) {
	O := (*wl.Object)(S)
	M := wl.NewMessage(0)
	M.WriteUint(uint32(edge))
	M.WriteHeader(O.GetId(), 9)
	O.Enqueue(M)

	if O.Debug() {
		M.DebugRequest(O.GetDisplay(), "set_exclusive_edge", uint32(edge))
	}
}
