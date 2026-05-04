package ui

import (
	"fmt"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// SettingsScreen is the per-section font configuration overlay. It mutates
// th.Fonts in place and fires onChange whenever the user picks a different
// face or size, so the host can persist to disk.
type SettingsScreen struct {
	th       *Theme
	onChange func()
	onClose  func()
	onClearCache func()

	rows           []*settingsRow
	accounts       []string
	defaultAccount *string
	renderImages   *bool
	darkModeLeft   *bool
	darkModeRight  *bool
	prevA          widget.Clickable
	nextA          widget.Clickable
	clearCache     widget.Clickable
	close          widget.Clickable
	list           widget.List
	imgToggle      widget.Bool
	leftDarkToggle widget.Bool
	rightDarkToggle widget.Bool
}

type settingsRow struct {
	label   string
	target  *FontStyle
	mono    bool
	prevF   widget.Clickable
	nextF   widget.Clickable
	smaller widget.Clickable
	bigger  widget.Clickable
}

func newSettingsScreen(th *Theme, accounts []string, defaultAccount *string, renderImages *bool, darkL, darkR *bool, onChange, onClose, onClearCache func()) *SettingsScreen {
	s := &SettingsScreen{
		th:             th,
		accounts:       accounts,
		defaultAccount: defaultAccount,
		renderImages:   renderImages,
		darkModeLeft:   darkL,
		darkModeRight:  darkR,
		onChange:       onChange,
		onClose:        onClose,
		onClearCache:   onClearCache,
	}
	s.imgToggle.Value = *renderImages
	s.leftDarkToggle.Value = *darkL
	s.rightDarkToggle.Value = *darkR
	s.list.Axis = layout.Vertical
	s.rows = []*settingsRow{
		{label: "Global Font (Base)", target: &th.Fonts.Global},
		{label: "List View", target: &th.Fonts.List},
		{label: "Message View", target: &th.Fonts.Message},
		{label: "Compose", target: &th.Fonts.Compose},
		{label: "Status Bar", target: &th.Fonts.StatusBar},
	}
	return s
}

// faces returns the typeface options offered for a given row. Code rows get
// the monospace-only subset so users don't accidentally pick a proportional
// face for code blocks.
func (s *SettingsScreen) faces(r *settingsRow) []string {
	if r.mono {
		return s.th.MonoFaces
	}
	return s.th.Faces
}

// cycleFace steps through the available typefaces by delta. Empty face
// (= "default") is treated as a virtual entry at index 0, so users can step
// back to the unset state.
func (s *SettingsScreen) cycleFace(r *settingsRow, delta int) {
	faces := s.faces(r)
	options := append([]string{""}, faces...)
	idx := 0
	for i, f := range options {
		if f == r.target.Face {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(options)) % len(options)
	r.target.Face = options[idx]
}

func (s *SettingsScreen) bumpSize(r *settingsRow, delta float32) {
	cur := r.target.Size
	if cur == 0 {
		cur = float32(s.th.TextSize)
	}
	cur += delta
	if cur < 8 {
		cur = 8
	}
	if cur > 32 {
		cur = 32
	}
	r.target.Size = cur
}

func (s *SettingsScreen) cycleAccount(delta int) {
	if len(s.accounts) == 0 {
		return
	}
	options := append([]string{""}, s.accounts...)
	idx := 0
	for i, a := range options {
		if a == *s.defaultAccount {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(options)) % len(options)
	*s.defaultAccount = options[idx]
}

func (s *SettingsScreen) Layout(gtx layout.Context) layout.Dimensions {
	th := s.th
	dirty := false

	if s.prevA.Clicked(gtx) {
		s.cycleAccount(-1)
		dirty = true
	}
	if s.nextA.Clicked(gtx) {
		s.cycleAccount(1)
		dirty = true
	}
	if s.clearCache.Clicked(gtx) {
		if s.onClearCache != nil {
			s.onClearCache()
		}
	}

	if s.imgToggle.Update(gtx) {
		*s.renderImages = s.imgToggle.Value
		dirty = true
	}
	if s.leftDarkToggle.Update(gtx) {
		*s.darkModeLeft = s.leftDarkToggle.Value
		dirty = true
	}
	if s.rightDarkToggle.Update(gtx) {
		*s.darkModeRight = s.rightDarkToggle.Value
		dirty = true
	}

	for _, r := range s.rows {
		if r.prevF.Clicked(gtx) {
			s.cycleFace(r, -1)
			dirty = true
		}
		if r.nextF.Clicked(gtx) {
			s.cycleFace(r, 1)
			dirty = true
		}
		if r.smaller.Clicked(gtx) {
			s.bumpSize(r, -1)
			dirty = true
		}
		if r.bigger.Clicked(gtx) {
			s.bumpSize(r, 1)
			dirty = true
		}
	}
	if s.close.Clicked(gtx) {
		if s.onClose != nil {
			s.onClose()
		}
	}
	if dirty && s.onChange != nil {
		s.onChange()
	}

	return paintedBg(gtx, th.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(20),
			Bottom: unit.Dp(20),
			Left:   unit.Dp(28),
			Right:  unit.Dp(28),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutHeader(gtx, th)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return material.List(th.Theme, &s.list).Layout(gtx, len(s.rows)+4, func(gtx layout.Context, i int) layout.Dimensions {
						if i == 0 {
							return s.layoutAccountSection(gtx, th)
						}
						if i == 1 {
							return s.layoutThemeSection(gtx, th)
						}
						if i == 2 {
							return s.layoutImageSection(gtx, th)
						}
						if i == 3 {
							return s.layoutCacheSection(gtx, th)
						}
						return s.layoutRow(gtx, th, s.rows[i-4])
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					hint := material.Caption(th.Theme, "Esc close · changes save automatically")
					hint.Color = th.Pal.TextMuted
					return hint.Layout(gtx)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutThemeSection(gtx layout.Context, th *Theme) layout.Dimensions {
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Theme, "Themes")
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(th.Theme, "Left Pane (List)")
							th.applyFont(&lbl, FontStyle{})
							lbl.Color = th.Pal.TextDim
							gtx.Constraints.Min.X = gtx.Dp(unit.Dp(150))
							return lbl.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							sw := material.Switch(th.Theme, &s.leftDarkToggle, "Dark Mode")
							sw.Color.Enabled = th.Pal.Accent
							return sw.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(th.Theme, "Right Pane (Message)")
							th.applyFont(&lbl, FontStyle{})
							lbl.Color = th.Pal.TextDim
							gtx.Constraints.Min.X = gtx.Dp(unit.Dp(150))
							return lbl.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							sw := material.Switch(th.Theme, &s.rightDarkToggle, "Dark Mode")
							sw.Color.Enabled = th.Pal.Accent
							return sw.Layout(gtx)
						}),
					)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutImageSection(gtx layout.Context, th *Theme) layout.Dimensions {
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Theme, "Images")
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sw := material.Switch(th.Theme, &s.imgToggle, "Render remote images")
					sw.Color.Enabled = th.Pal.Accent
					return sw.Layout(gtx)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutCacheSection(gtx layout.Context, th *Theme) layout.Dimensions {
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Theme, "Local Cache")
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.button(gtx, th, &s.clearCache, "clear all")
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutAccountSection(gtx layout.Context, th *Theme) layout.Dimensions {
	addr := *s.defaultAccount
	if addr == "" {
		addr = "none (last used)"
	}
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Theme, "Default Account")
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(th.Theme, "Email")
							th.applyFont(&lbl, FontStyle{})
							lbl.Color = th.Pal.TextDim
							gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
							return lbl.Layout(gtx)
						}),
						layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &s.prevA, "<") }),
						layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body1(th.Theme, addr)
							th.applyFont(&lbl, FontStyle{})
							lbl.Color = th.Pal.Text
							return lbl.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &s.nextA, ">") }),
					)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutHeader(gtx layout.Context, th *Theme) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.H6(th.Theme, "Settings · Fonts")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.Accent
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return s.button(gtx, th, &s.close, "close")
		}),
	)
}

func (s *SettingsScreen) layoutRow(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Theme, r.label)
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutFaceControls(gtx, th, r)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutSizeControls(gtx, th, r)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutPreview(gtx, th, r)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutFaceControls(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	face := r.target.Face
	if face == "" {
		face = "default"
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(th.Theme, "Face")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.TextDim
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.prevF, "<") }),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(th.Theme, face)
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.Text
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.nextF, ">") }),
	)
}

func (s *SettingsScreen) layoutSizeControls(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	size := r.target.Size
	display := fmt.Sprintf("%.0f sp", size)
	if size == 0 {
		display = fmt.Sprintf("default (%d sp)", int(s.th.TextSize))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(th.Theme, "Size")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.TextDim
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.smaller, "-") }),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(th.Theme, display)
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.Text
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.bigger, "+") }),
	)
}

// layoutPreview shows a sample line in the row's current face/size so changes
// are visible before the user closes the screen.
func (s *SettingsScreen) layoutPreview(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	sample := "The quick brown fox jumps over the lazy dog"
	if r.mono {
		sample = "for i := 0; i < n; i++ { fmt.Println(i) }"
	}
	lbl := material.Body1(th.Theme, sample)
	lbl.Color = th.Pal.TextDim
	th.applyFont(&lbl, *r.target)
	return lbl.Layout(gtx)
}

func (s *SettingsScreen) button(gtx layout.Context, th *Theme, c *widget.Clickable, label string) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return withBorder(gtx, th.Pal.Border, borders{Top: true, Bottom: true, Left: true, Right: true}, func(gtx layout.Context) layout.Dimensions {
			return paintedBg(gtx, th.Pal.StatusBg, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top:    unit.Dp(4),
					Bottom: unit.Dp(4),
					Left:   unit.Dp(10),
					Right:  unit.Dp(10),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(th.Theme, label)
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.Text
					return lbl.Layout(gtx)
				})
			})
		})
	})
}
