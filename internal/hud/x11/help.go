package x11

import (
	"image"
	"image/color"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/xevent"
	"github.com/jezek/xgbutil/xgraphics"
	"github.com/jezek/xgbutil/xwindow"
)

// helpTongueID is the synthetic sessionView.ID for the help "tongue" at
// the top of the dock. The click dispatcher checks this constant to route
// clicks to the help window instead of the visor IPC socket.
const helpTongueID = "__visor_help__"

// helpTongueSession is the synthesized state for the help tab. Activity/
// attention are chosen so colorFor() returns the neutral grey.
var helpTongueSession = sessionView{
	ID:         helpTongueID,
	Activity:   "waiting",
	Attention:  "ack",
	DisplayCWD: "?  Help",
}

// helpRow is one line in the help screen.
type helpRow struct {
	heading bool   // formats the label as a section heading
	swatch  uint32 // 0 = no swatch column for this row
	label   string
	indent  bool // small indent for legend rows under a heading
}

// helpContent returns the rows shown in the help screen. Legend colors are
// pulled live from colorFor() so changes there stay in sync.
func helpContent() []helpRow {
	col := func(act, att, wait string) uint32 {
		return colorFor(sessionView{Activity: act, Attention: att, Waiting: wait})
	}
	return []helpRow{
		{heading: true, label: "Visor — Claude session dock"},
		{label: ""},
		{heading: true, label: "Colors"},
		{indent: true, swatch: col("working", "ack", ""), label: "working"},
		{indent: true, swatch: col("waiting", "needs", "user"), label: "waiting for you"},
		{indent: true, swatch: col("waiting", "needs", "permission"), label: "needs permission"},
		{indent: true, swatch: col("waiting", "ack", "user"), label: "idle (acknowledged)"},
		{indent: true, swatch: col("waiting", "dismissed", "user"), label: "dismissed (silenced)"},
		{label: ""},
		{heading: true, label: "Clicks"},
		{indent: true, label: "left   →  jump to session"},
		{indent: true, label: "middle →  acknowledge"},
		{indent: true, label: "right  →  dismiss until next state change"},
		{label: ""},
		{heading: true, label: "Behavior"},
		{indent: true, label: "hover a tab to see its working dir"},
		{indent: true, label: "busy sessions wobble"},
		{label: ""},
		{label: "(click anywhere to close)"},
	}
}

// Help window geometry and colors.
const (
	helpW      = 460
	helpH      = 340
	helpBg     = 0x14_16_1c
	helpFg     = 0xe5_e9_f0
	helpHead   = 0x88_c0_d0
	helpRowH   = 18
	helpLeft   = 22
	helpIndent = 14
	helpTop    = 26
	swatchSize = 10
	swatchPad  = 8
)

// helpWindow is the centered popup that explains the dock's visual language.
type helpWindow struct {
	X   *xgbutil.XUtil
	win *xwindow.Window
	im  *xgraphics.Image
}

// openHelp creates and maps the centered help window. Returns nil with an
// error if the X window can't be created.
func openHelp(X *xgbutil.XUtil, mon monitor, font *truetype.Font, onClose func()) (*helpWindow, error) {
	win, err := xwindow.Generate(X)
	if err != nil {
		return nil, err
	}
	x := mon.x + (mon.w-helpW)/2
	y := mon.y + (mon.h-helpH)/2
	if err := win.CreateChecked(
		X.RootWin(),
		x, y, helpW, helpH,
		xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		uint32(helpBg),
		1,
		uint32(xproto.EventMaskButtonPress|xproto.EventMaskExposure),
	); err != nil {
		return nil, err
	}
	_ = ewmh.WmWindowTypeSet(X, win.Id, []string{"_NET_WM_WINDOW_TYPE_DIALOG"})
	_ = ewmh.WmStateSet(X, win.Id, []string{
		"_NET_WM_STATE_ABOVE",
		"_NET_WM_STATE_STICKY",
	})
	_ = ewmh.WmNameSet(X, win.Id, "visor-help")

	h := &helpWindow{X: X, win: win}
	h.render(font)
	xevent.ButtonPressFun(func(X *xgbutil.XUtil, ev xevent.ButtonPressEvent) {
		h.close()
		if onClose != nil {
			onClose()
		}
	}).Connect(X, win.Id)

	win.Map()
	return h, nil
}

func (h *helpWindow) render(font *truetype.Font) {
	im := xgraphics.New(h.X, image.Rect(0, 0, helpW, helpH))
	bg := rgba(helpBg)
	for y := 0; y < helpH; y++ {
		for x := 0; x < helpW; x++ {
			im.Set(x, y, bg)
		}
	}

	if font != nil {
		fg := rgba(helpFg)
		head := rgba(helpHead)
		y := helpTop
		for _, r := range helpContent() {
			textX := helpLeft
			if r.indent {
				textX += helpIndent + swatchSize + swatchPad
				if r.swatch != 0 {
					sx := helpLeft + helpIndent
					sy := y - swatchSize - 1
					fillRect(im, sx, sy, swatchSize, swatchSize, rgba(r.swatch))
				}
			}
			c := fg
			size := 11.0
			if r.heading {
				c = head
				size = 12.5
			}
			if r.label != "" {
				_, _, _ = im.Text(textX, y-12, c, size, font, r.label)
			}
			y += helpRowH
		}
	}

	im.CreatePixmap()
	im.XDraw()
	im.XSurfaceSet(h.win.Id)
	xproto.ClearArea(h.X.Conn(), false, h.win.Id, 0, 0, helpW, helpH)
	if h.im != nil {
		h.im.Destroy()
	}
	h.im = im
}

func (h *helpWindow) close() {
	if h.im != nil {
		h.im.Destroy()
		h.im = nil
	}
	if h.win != nil {
		h.win.Destroy()
		h.win = nil
	}
}

// fillRect paints a solid rectangle into an xgraphics image.
func fillRect(im *xgraphics.Image, x, y, w, h int, c color.RGBA) {
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			im.Set(x+xx, y+yy, c)
		}
	}
}

