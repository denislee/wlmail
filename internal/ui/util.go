package ui

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// paintedBg fills the area occupied by w with bg, then renders w on top.
func paintedBg(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()

	rect := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	paint.ColorOp{Color: bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	rect.Pop()

	call.Add(gtx.Ops)
	return dims
}

// borders bundles which sides of a rectangle to stroke. Zero value = no border.
type borders struct {
	Top, Right, Bottom, Left bool
}

// withBorder runs w, then draws 1dp lines on the requested edges of its
// dimensions in c.
func withBorder(gtx layout.Context, c color.NRGBA, b borders, w layout.Widget) layout.Dimensions {
	dims := w(gtx)
	px := gtx.Dp(unit.Dp(1))
	if px < 1 {
		px = 1
	}
	sz := dims.Size
	stroke := func(r image.Rectangle) {
		if r.Dx() <= 0 || r.Dy() <= 0 {
			return
		}
		defer clip.Rect{Min: r.Min, Max: r.Max}.Push(gtx.Ops).Pop()
		paint.ColorOp{Color: c}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}
	if b.Top {
		stroke(image.Rect(0, 0, sz.X, px))
	}
	if b.Bottom {
		stroke(image.Rect(0, sz.Y-px, sz.X, sz.Y))
	}
	if b.Left {
		stroke(image.Rect(0, 0, px, sz.Y))
	}
	if b.Right {
		stroke(image.Rect(sz.X-px, 0, sz.X, sz.Y))
	}
	return dims
}

// Flow is a layout that places widgets horizontally, wrapping to the next line
// when they exceed the available width.
type Flow struct{}

func (f Flow) Layout(gtx layout.Context, count int, w func(gtx layout.Context, i int) layout.Dimensions) layout.Dimensions {
	var dims layout.Dimensions
	var x, y int
	var maxH int

	for i := 0; i < count; i++ {
		m := op.Record(gtx.Ops)
		childGtx := gtx
		childGtx.Constraints.Min = image.Point{}
		// We subtract current x from Max.X to see how much is left on this line.
		// However, if we want to allow the child to wrap *within itself* if it's too long
		// for any line, we should give it the full width as a maximum.
		childGtx.Constraints.Max.X = gtx.Constraints.Max.X
		d := w(childGtx, i)
		call := m.Stop()

		// If this child doesn't fit on the current line, wrap to next line.
		// Exception: if we are already at x=0, we can't wrap further, so just place it.
		maxWidth := gtx.Constraints.Max.X
		if maxWidth > 2000 {
			// A reasonable fallback for width if we're in an unconstrained container.
			maxWidth = 800
		}
		if x > 0 && x+d.Size.X > maxWidth {
			x = 0
			y += maxH
			maxH = 0
		}

		trans := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		trans.Pop()

		x += d.Size.X
		if d.Size.Y > maxH {
			maxH = d.Size.Y
		}
		if x > dims.Size.X {
			dims.Size.X = x
		}
	}
	dims.Size.Y = y + maxH
	if dims.Size.X < gtx.Constraints.Min.X {
		dims.Size.X = gtx.Constraints.Min.X
	}
	if dims.Size.Y < gtx.Constraints.Min.Y {
		dims.Size.Y = gtx.Constraints.Min.Y
	}
	// Final width should not exceed constraints
	if dims.Size.X > gtx.Constraints.Max.X {
		dims.Size.X = gtx.Constraints.Max.X
	}
	return dims
}
