package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/ewmh"
	"github.com/BurntSushi/xgbutil/icccm"
	"github.com/BurntSushi/xgbutil/xevent"
	"github.com/BurntSushi/xgbutil/xprop"

	"github.com/BurntSushi/wingo/focus"
	"github.com/BurntSushi/wingo/frame"
	"github.com/BurntSushi/wingo/logger"
)

var (
	modes = map[byte]string{
		xproto.NotifyModeNormal:       "NotifyNormal",
		xproto.NotifyModeGrab:         "NotifyGrab",
		xproto.NotifyModeUngrab:       "NotifyUngrab",
		xproto.NotifyModeWhileGrabbed: "NotifyWhileGrabbed",
	}

	details = map[byte]string{
		xproto.NotifyDetailAncestor:         "NotifyAncestor",
		xproto.NotifyDetailVirtual:          "NotifyVirtual",
		xproto.NotifyDetailInferior:         "NotifyInferior",
		xproto.NotifyDetailNonlinear:        "NotifyNonlinear",
		xproto.NotifyDetailNonlinearVirtual: "NotifyNonlinearVirtual",
		xproto.NotifyDetailPointer:          "NotifyPointer",
		xproto.NotifyDetailPointerRoot:      "NotifyPointerRoot",
		xproto.NotifyDetailNone:             "NotifyNone",
	}
)

func (c *client) attachEventCallbacks() {
	c.win.Listen(xproto.EventMaskPropertyChange |
		xproto.EventMaskStructureNotify)

	c.Frame().Parent().Listen(xproto.EventMaskFocusChange |
		xproto.EventMaskSubstructureRedirect)

	c.cbUnmapNotify().Connect(c.X, c.Id())
	c.cbDestroyNotify().Connect(c.X, c.Id())
	c.cbConfigureRequest().Connect(c.X, c.Id())
	c.cbPropertyNotify().Connect(c.X, c.Id())

	c.handleFocusIn().Connect(c.X, c.Frame().Parent().Id)
	c.handleFocusOut().Connect(c.X, c.Frame().Parent().Id)

	c.clientMouseConfig()
	c.frameMouseConfig()
}

func (c *client) cbDestroyNotify() xevent.DestroyNotifyFun {
	f := func(X *xgbutil.XUtil, ev xevent.DestroyNotifyEvent) {
		c.unmanage()
	}
	return xevent.DestroyNotifyFun(f)
}

func (c *client) cbUnmapNotify() xevent.UnmapNotifyFun {
	f := func(X *xgbutil.XUtil, ev xevent.UnmapNotifyEvent) {
		// If a client has "iconified" set to true and we get an UnmapNotify
		// event, then that means we need to switch focus to the next window
		// in the focus stack.
		if c.iconified && focus.Current().Id() == c.Id() {
			wingo.focusFallback()
		}

		// When a client issues an Unmap request, the window manager should
		// unmanage it. However, when wingo unmaps the window, we shouldn't
		// unmanage it. Thus, every time wingo unmaps the window, the
		// unmapIgnore counter is incremented. Only when it is zero does it mean
		// that we should unmanage the client (i.e., the unmap request came
		// from somewhere other than Wingo.)
		if c.unmapIgnore > 0 {
			c.unmapIgnore--
			return
		}
		c.unmanage()
	}
	return xevent.UnmapNotifyFun(f)
}

func (c *client) cbConfigureRequest() xevent.ConfigureRequestFun {
	f := func(X *xgbutil.XUtil, ev xevent.ConfigureRequestEvent) {
		if c.frame.Moving() || c.frame.Resizing() || c.maximized {
			logger.Lots.Printf("Denying ConfigureRequest from client because " +
				"the client is in the processing of moving/resizing, or is " +
				"maximized.")

			// As per ICCCM 4.1.5, a window that has not been moved or resized
			// must receive a synthetic ConfigureNotify event.
			c.sendConfigureNotify()
			return
		}

		flags := int(ev.ValueMask) &
			^int(xproto.ConfigWindowStackMode) &
			^int(xproto.ConfigWindowSibling)
		x, y, w, h := frame.ClientToFrame(c.frame,
			int(ev.X), int(ev.Y), int(ev.Width), int(ev.Height))
		c.LayoutMROpt(flags, x, y, w, h)
	}
	return xevent.ConfigureRequestFun(f)
}

func (c *client) sendConfigureNotify() {
	geom := c.Frame().Geom()
	ev := xproto.ConfigureNotifyEvent{
		Event:            c.Id(),
		Window:           c.Id(),
		AboveSibling:     0,
		X:                int16(geom.X()),
		Y:                int16(geom.Y()),
		Width:            uint16(c.win.Geom.Width()),
		Height:           uint16(c.win.Geom.Height()),
		BorderWidth:      0,
		OverrideRedirect: false,
	}
	xproto.SendEvent(c.X.Conn(), false, c.Id(),
		xproto.EventMaskStructureNotify, string(ev.Bytes()))
}

func (c *client) cbPropertyNotify() xevent.PropertyNotifyFun {
	// helper function to log property vals
	showVals := func(o, n interface{}) {
		logger.Lots.Printf("\tOld value: '%s', new value: '%s'", o, n)
	}
	f := func(X *xgbutil.XUtil, ev xevent.PropertyNotifyEvent) {
		name, err := xprop.AtomName(c.X, ev.Atom)
		if err != nil {
			logger.Warning.Printf("Could not get property atom name for '%s' "+
				"because: %s.", ev, err)
		}

		logger.Lots.Printf("Updating property %s with state %v on window %s",
			name, ev.State, c)
		switch name {
		case "_NET_WM_VISIBLE_NAME":
			fallthrough
		case "_NET_WM_NAME":
			fallthrough
		case "WM_NAME":
			c.refreshName()
		case "_NET_WM_ICON":
		case "WM_HINTS":
			if hints, err := icccm.WmHintsGet(X, c.Id()); err == nil {
				c.hints = hints
			}
		case "WM_NORMAL_HINTS":
			if nhints, err := icccm.WmNormalHintsGet(X, c.Id()); err == nil {
				c.nhints = nhints
			}
		case "WM_TRANSIENT_FOR":
			if trans, err := icccm.WmTransientForGet(X, c.Id()); err == nil {
				if transCli := wingo.findManagedClient(trans); transCli != nil {
					c.transientFor = transCli
				}
			}
		case "_NET_WM_USER_TIME":
			if newTime, err := ewmh.WmUserTimeGet(X, c.Id()); err == nil {
				showVals(c.time, newTime)
				c.time = xproto.Timestamp(newTime)
			}
		case "_NET_WM_STRUT_PARTIAL":
		}

	}
	return xevent.PropertyNotifyFun(f)
}

func ignoreFocus(modeByte, detailByte byte) bool {
	mode, detail := modes[modeByte], details[detailByte]

	if mode == "NotifyGrab" || mode == "NotifyUngrab" {
		return true
	}
	if detail == "NotifyAncestor" ||
		detail == "NotifyInferior" ||
		detail == "NotifyNonlinear" ||
		detail == "NotifyPointer" ||
		detail == "NotifyPointerRoot" ||
		detail == "NotifyNone" {

		return true
	}
	// Only accept modes: NotifyNormal and NotifyWhileGrabbed
	// Only accept details: NotifyVirtual, NotifyNonlinearVirtual
	return false
}

func ignoreRootFocus(modeByte, detailByte byte) bool {
	mode, detail := modes[modeByte], details[detailByte]

	if mode == "NotifyGrab" || mode == "NotifyUngrab" {
		return true
	}
	if detail == "NotifyAncestor" ||
		detail == "NotifyInferior" ||
		detail == "NotifyVirtual" ||
		detail == "NotifyNonlinear" ||
		detail == "NotifyNonlinearVirtual" ||
		detail == "NotifyPointer" {

		return true
	}
	// Only accept modes: NotifyNormal and NotifyWhileGrabbed
	// Only accept details: NotifyPointerRoot, NotifyNone
	return false
}

func (c *client) handleFocusIn() xevent.FocusInFun {
	f := func(X *xgbutil.XUtil, ev xevent.FocusInEvent) {
		if ignoreFocus(ev.Mode, ev.Detail) {
			return
		}

		c.Focused()
		// logger.Debug.Println("---------------------------------------------")
		// logger.Debug.Println("Focus In") 
		// logger.Debug.Printf("Window: %s", c.Name()) 
		// logger.Debug.Printf("Mode: %s", modes[ev.Mode]) 
		// logger.Debug.Printf("Detail: %s", details[ev.Detail]) 
		// logger.Debug.Println("---------------------------------------------")
	}
	return xevent.FocusInFun(f)
}

func (c *client) handleFocusOut() xevent.FocusOutFun {
	f := func(X *xgbutil.XUtil, ev xevent.FocusOutEvent) {
		if ignoreFocus(ev.Mode, ev.Detail) {
			return
		}

		c.Unfocused()
		// logger.Debug.Println("---------------------------------------------")
		// logger.Debug.Println("Focus Out") 
		// logger.Debug.Printf("Window: %s", c.Name()) 
		// logger.Debug.Printf("Mode: %s", modes[ev.Mode]) 
		// logger.Debug.Printf("Detail: %s", details[ev.Detail]) 
		// logger.Debug.Println("---------------------------------------------")
	}
	return xevent.FocusOutFun(f)
}
