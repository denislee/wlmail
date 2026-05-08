package ui

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"wlmail/internal/keys"
	"wlmail/internal/mail"
)

func (a *App) layout(gtx layout.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Background.
	a.th.SetDarkMode(a.settings.DarkModeLeft) // Default for header/status
	paint.FillShape(gtx.Ops, a.th.Pal.Bg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutHeader),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			switch a.view {
			case viewList, viewMessage:
				return a.layoutSplit(gtx)
			case viewAccounts:
				return a.layoutAccounts(gtx)
			case viewLinks:
				return a.layoutLinks(gtx)
			case viewCompose:
				return a.layoutCompose(gtx)
			case viewHelp:
				return a.layoutHelp(gtx)
			case viewSettings:
				return a.layoutSettings(gtx)
			}
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Rigid(a.layoutStatus),
	)
}

func (a *App) layoutSplit(gtx layout.Context) layout.Dimensions {
	if a.focus == paneMessage {
		a.th.SetDarkMode(a.settings.DarkModeRight)
		return paintedBg(gtx, a.th.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
			return a.layoutMessage(gtx)
		})
	}

	totalWidth := float32(gtx.Constraints.Max.X)
	barWidth := gtx.Dp(unit.Dp(6))
	leftWidth := a.settings.SplitRatio * totalWidth

	// Handle resize dragging at the split container level for stable coordinates
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.splitDrag)

	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target: &a.splitDrag,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release,
		})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			if e.Position.X >= leftWidth && e.Position.X <= leftWidth+float32(barWidth) {
				a.splitDrag = true
				a.splitDragX = e.Position.X
			}
		case pointer.Drag:
			if a.splitDrag {
				deltaX := e.Position.X - a.splitDragX
				a.splitDragX = e.Position.X
				a.settings.SplitRatio += deltaX / totalWidth
				if a.settings.SplitRatio < 0.1 {
					a.settings.SplitRatio = 0.1
				}
				if a.settings.SplitRatio > 0.9 {
					a.settings.SplitRatio = 0.9
				}
			}
		case pointer.Release:
			a.splitDrag = false
			_ = a.saveSettings()
		}
	}
	area.Pop()

	leftWeight := a.settings.SplitRatio
	rightWeight := 1.0 - leftWeight

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Flexed(leftWeight, func(gtx layout.Context) layout.Dimensions {
			a.th.SetDarkMode(a.settings.DarkModeLeft)
			return withBorder(gtx, a.th.Pal.Accent, borders{Right: a.focus == paneList}, func(gtx layout.Context) layout.Dimensions {
				return paintedBg(gtx, a.th.Pal.BgLeft, func(gtx layout.Context) layout.Dimensions {
					return a.layoutList(gtx)
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			sz := image.Pt(barWidth, gtx.Constraints.Max.Y)
			// Draw the visible line in the center of the grab area
			lineW := max(gtx.Dp(unit.Dp(1)), 1)
			paint.FillShape(gtx.Ops, a.th.Pal.Border, clip.Rect{
				Min: image.Pt((sz.X-lineW)/2, 0),
				Max: image.Pt((sz.X+lineW)/2, sz.Y),
			}.Op())

			// Show resize cursor when hovering over the bar
			area := clip.Rect{Max: sz}.Push(gtx.Ops)
			pointer.CursorColResize.Add(gtx.Ops)
			area.Pop()

			return layout.Dimensions{Size: sz}
		}),
		layout.Flexed(rightWeight, func(gtx layout.Context) layout.Dimensions {
			a.th.SetDarkMode(a.settings.DarkModeRight)
			return withBorder(gtx, a.th.Pal.Accent, borders{Left: a.focus == paneMessage}, func(gtx layout.Context) layout.Dimensions {
				return paintedBg(gtx, a.th.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
					return a.layoutMessage(gtx)
				})
			})
		}),
	)
}

func (a *App) layoutSettings(gtx layout.Context) layout.Dimensions {
	return a.settingsScreen.Layout(gtx)
}

func (a *App) layoutHeader(gtx layout.Context) layout.Dimensions {
	return withBorder(gtx, a.th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				title := folders[a.folderIdx].name
				if a.searchQ != "" {
					title = "search: " + a.searchQ
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						brand := material.Label(a.th.Theme, unit.Sp(14), "wlmail")
						brand.Color = a.th.Pal.Accent
						brand.Font.Weight = font.Bold
						return brand.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sep := material.Label(a.th.Theme, unit.Sp(14), "  ·  ")
						sep.Color = a.th.Pal.TextMuted
						return sep.Layout(gtx)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(a.th.Theme, unit.Sp(13), strings.ToLower(title))
						lbl.Color = a.th.Pal.TextStrong
						
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(lbl.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if !a.loading {
									return layout.Dimensions{}
								}
								gtx.Constraints.Min = image.Pt(gtx.Dp(unit.Dp(16)), gtx.Dp(unit.Dp(16)))
								gtx.Constraints.Max = gtx.Constraints.Min
								return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									loader := material.Loader(a.th.Theme)
									loader.Color = a.th.Pal.Accent
									return loader.Layout(gtx)
								})
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if a.email == "" {
							return layout.Dimensions{}
						}
						lbl := material.Label(a.th.Theme, unit.Sp(12), a.email)
						lbl.Color = a.th.Pal.TextDim
						return lbl.Layout(gtx)
					}),
				)
			})
	})
}

func (a *App) layoutStatus(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(unit.Dp(24))
	dims := layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
	paint.FillShape(gtx.Ops, a.th.Pal.StatusBg, clip.Rect{Max: dims.Size}.Op())
	// Hairline top border separates status from content.
	bx := max(gtx.Dp(unit.Dp(1)), 1)
	paint.FillShape(gtx.Ops, a.th.Pal.Border, clip.Rect{Max: image.Pt(dims.Size.X, bx)}.Op())

	modeText, modeColor := a.modeIndicator()
	right := a.binder.Pending()
	return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10), Top: unit.Dp(4)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if modeText == "" {
						return layout.Dimensions{}
					}
					lbl := material.Label(a.th.Theme, unit.Sp(11), modeText+"  ")
					lbl.Color = modeColor
					lbl.Font.Weight = font.Bold
					a.th.applyFont(&lbl, a.th.Fonts.StatusBar)
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if a.mode == keys.ModeSearch {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(a.coloredText("/", a.th.Pal.Accent, a.th.Fonts.StatusBar)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								// Drain submit events; runSearch is invoked
								// from the UI event loop above.
								for {
									ev, ok := a.searchBuf.Update(gtx)
									if !ok {
										break
									}
									if _, ok := ev.(widget.SubmitEvent); ok {
										a.searchTriggered = true
									}
								}
								e := material.Editor(a.th.Theme, &a.searchBuf, "filter…")
								e.Color = a.th.Pal.Text
								e.HintColor = a.th.Pal.TextDim
								a.th.applyEditorFont(&e, a.th.Fonts.StatusBar)
								return e.Layout(gtx)
							}),
						)
					}
					lbl := material.Label(a.th.Theme, unit.Sp(12), a.status)
					a.th.applyFont(&lbl, a.th.Fonts.StatusBar)
					lbl.Color = a.th.Pal.TextDim
					return lbl.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(a.th.Theme, unit.Sp(12), right)
					a.th.applyFont(&lbl, a.th.Fonts.StatusBar)
					lbl.Color = a.th.Pal.Accent
					return lbl.Layout(gtx)
				}),
			)
		})
}

func (a *App) layoutList(gtx layout.Context) layout.Dimensions {
	items := a.items
	cursor := a.cursor

	if len(items) == 0 {
		return centerLabel(gtx, a.th, "(no messages — press R to refresh)")
	}

	// Keep cursor visible.
	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 1.8))
	visible := gtx.Constraints.Max.Y / rowH
	if visible <= 0 {
		visible = 1
	}
	if cursor < a.scroll {
		a.scroll = cursor
	}
	if cursor >= a.scroll+visible {
		a.scroll = cursor - visible + 1
	}
	if a.scroll < 0 {
		a.scroll = 0
	}

	end := a.scroll + visible
	if end > len(items) {
		end = len(items)
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Flexed(1,
		func(gtx layout.Context) layout.Dimensions {
			var children []layout.FlexChild
			for i := a.scroll; i < end; i++ {
				idx := i
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return a.layoutListRow(gtx, idx, items[idx])
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		},
	))
}

func (a *App) layoutListRow(gtx layout.Context, idx int, s mail.Summary) layout.Dimensions {
	cursor := a.cursor

	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 1.8))
	macro := image.Pt(gtx.Constraints.Max.X, rowH)
	if idx == cursor {
		paint.FillShape(gtx.Ops, a.th.Pal.BgRowSelected, clip.Rect{Max: macro}.Op())
		bar := max(gtx.Dp(unit.Dp(2)), 1)
		paint.FillShape(gtx.Ops, a.th.Pal.Accent, clip.Rect{Max: image.Pt(bar, rowH)}.Op())
	}
	star := "  "
	if s.Starred {
		star = "★ "
	}
	flag := "  "
	if s.Unread {
		flag = "● "
	}
	from := truncate(s.From, 16)
	subj := s.Subject
	age := relTime(s.Date)

	return layout.Inset{Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = rowH
			gtx.Constraints.Max.Y = rowH
			fromColor := a.th.Pal.TextDim
			if s.Unread {
				fromColor = a.th.Pal.TextStrong
			}
			return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceAround}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.coloredText(star, a.th.Pal.Star, a.th.Fonts.List)),
						layout.Rigid(a.coloredText(flag, a.th.Pal.Unread, a.th.Fonts.List)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.X = gtx.Dp(unit.Dp(100))
							gtx.Constraints.Max.X = gtx.Dp(unit.Dp(100))
							return a.coloredText(from, fromColor, a.th.Fonts.List)(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(a.th.Theme, unit.Sp(fontSize), subj)
							lbl.Color = a.th.Pal.Text
							a.th.applyFont(&lbl, a.th.Fonts.List)
							if s.Unread {
								lbl.Font.Weight = font.Bold
							}
							lbl.MaxLines = 1
							lbl.Truncator = "…"
							return lbl.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, a.coloredText(age, a.th.Pal.TextDim, a.th.Fonts.List))
						}),
					)
				}),
			)
		})
}

func (a *App) layoutAccounts(gtx layout.Context) layout.Dimensions {
	if len(a.accounts) == 0 {
		return centerLabel(gtx, a.th, "(no accounts — run wlmail -add for more)")
	}

	// Keep cursor visible.
	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 1.8))
	visible := gtx.Constraints.Max.Y / rowH
	if visible <= 0 {
		visible = 1
	}
	if a.cursor < a.scroll {
		a.scroll = a.cursor
	}
	if a.cursor >= a.scroll+visible {
		a.scroll = a.cursor - visible + 1
	}
	if a.scroll < 0 {
		a.scroll = 0
	}

	end := a.scroll + visible
	if end > len(a.accounts) {
		end = len(a.accounts)
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: unit.Dp(10), Top: unit.Dp(10), Left: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(a.th.Theme, unit.Sp(16), "Select Account")
				l.Color = a.th.Pal.Accent
				l.Font.Weight = font.Bold
				return l.Layout(gtx)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			var children []layout.FlexChild
			for i := a.scroll; i < end; i++ {
				idx := i
				addr := a.accounts[i]
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return a.layoutAccountRow(gtx, idx, addr)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}),
	)
}

func (a *App) layoutAccountRow(gtx layout.Context, idx int, email string) layout.Dimensions {
	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 1.8))
	macro := image.Pt(gtx.Constraints.Max.X, rowH)
	if idx == a.cursor {
		paint.FillShape(gtx.Ops, a.th.Pal.BgRowSelected, clip.Rect{Max: macro}.Op())
		bar := max(gtx.Dp(unit.Dp(2)), 1)
		paint.FillShape(gtx.Ops, a.th.Pal.Accent, clip.Rect{Max: image.Pt(bar, rowH)}.Op())
	}
	indicator := "  "
	if email == a.email {
		indicator = "● "
	}

	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(12)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = rowH
			gtx.Constraints.Max.Y = rowH
			return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceAround}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.coloredText(indicator, a.th.Pal.Accent, a.th.Fonts.List)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(a.th.Theme, unit.Sp(fontSize+1), email)
							lbl.Color = a.th.Pal.Text
							a.th.applyFont(&lbl, a.th.Fonts.List)
							if email == a.email {
								lbl.Font.Weight = font.Bold
							}
							return lbl.Layout(gtx)
						}),
					)
				}),
			)
		})
}

func (a *App) layoutLinks(gtx layout.Context) layout.Dimensions {
	if len(a.links) == 0 {
		return centerLabel(gtx, a.th, "(no links)")
	}

	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 3.6))
	visible := gtx.Constraints.Max.Y / rowH
	if visible <= 0 {
		visible = 1
	}
	if a.linkCursor < a.linkScroll {
		a.linkScroll = a.linkCursor
	}
	if a.linkCursor >= a.linkScroll+visible {
		a.linkScroll = a.linkCursor - visible + 1
	}
	if a.linkScroll < 0 {
		a.linkScroll = 0
	}

	end := a.linkScroll + visible
	if end > len(a.links) {
		end = len(a.links)
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: unit.Dp(10), Top: unit.Dp(10), Left: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(a.th.Theme, unit.Sp(16), "Links in message")
				l.Color = a.th.Pal.Accent
				l.Font.Weight = font.Bold
				return l.Layout(gtx)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			var children []layout.FlexChild
			for i := a.linkScroll; i < end; i++ {
				idx := i
				link := a.links[i]
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return a.layoutLinkRow(gtx, idx, link)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}),
	)
}

func (a *App) layoutLinkRow(gtx layout.Context, idx int, link linkItem) layout.Dimensions {
	fontSize := a.th.Fonts.List.Size
	if fontSize == 0 {
		fontSize = a.th.Fonts.Global.Size
	}
	if fontSize == 0 {
		fontSize = float32(a.th.TextSize)
	}
	rowH := gtx.Dp(unit.Dp(fontSize * 3.6))
	macro := image.Pt(gtx.Constraints.Max.X, rowH)
	if idx == a.linkCursor {
		paint.FillShape(gtx.Ops, a.th.Pal.BgRowSelected, clip.Rect{Max: macro}.Op())
		bar := max(gtx.Dp(unit.Dp(2)), 1)
		paint.FillShape(gtx.Ops, a.th.Pal.Accent, clip.Rect{Max: image.Pt(bar, rowH)}.Op())
	}

	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(12), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = rowH - gtx.Dp(unit.Dp(8))
			gtx.Constraints.Max.Y = rowH - gtx.Dp(unit.Dp(8))
			return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceAround}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Start}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(a.th.Theme, unit.Sp(fontSize), truncate(link.text, 80))
							lbl.Color = a.th.Pal.TextStrong
							a.th.applyFont(&lbl, a.th.Fonts.List)
							return lbl.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(a.th.Theme, unit.Sp(fontSize), link.url)
							lbl.Color = a.th.Pal.Link
							a.th.applyFont(&lbl, a.th.Fonts.List)
							lbl.MaxLines = 1
							lbl.Truncator = "…"
							return lbl.Layout(gtx)
						}),
					)
				}),
			)
		})
}

func (a *App) layoutMessage(gtx layout.Context) layout.Dimensions {
	if a.message == nil {
		return centerLabel(gtx, a.th, "(no message)")
	}
	m := a.message
	return layout.Inset{Left: unit.Dp(20), Right: unit.Dp(20), Top: unit.Dp(14), Bottom: unit.Dp(10)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(a.th.Theme, unit.Sp(17), m.Subject)
					lbl.Color = a.th.Pal.TextStrong
					lbl.Font.Weight = font.Bold
					a.th.applyFont(&lbl, a.th.Fonts.Message)
					return lbl.Layout(gtx)
				}),
				layout.Rigid(spacer(gtx, 6)),
				layout.Rigid(a.messageHeaderRow("from", m.From)),
				layout.Rigid(a.messageHeaderRow("to", m.To)),
				layout.Rigid(spacer(gtx, 10)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					px := max(gtx.Dp(unit.Dp(1)), 1)
					sz := image.Pt(gtx.Constraints.Max.X, px)
					paint.FillShape(gtx.Ops, a.th.Pal.Border, clip.Rect{Max: sz}.Op())
					return layout.Dimensions{Size: sz}
				}),
				layout.Rigid(spacer(gtx, 10)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					// Rich body: group spans into paragraphs separated by "\n" markers.
					var paragraphs [][]mail.Span
					var current []mail.Span
					for _, s := range m.Body {
						if s.Text == "\n" {
							if len(current) > 0 {
								paragraphs = append(paragraphs, current)
								current = nil
							}
							paragraphs = append(paragraphs, []mail.Span{s})
						} else {
							current = append(current, s)
						}
					}
					if len(current) > 0 {
						paragraphs = append(paragraphs, current)
					}

					a.messageList.Axis = layout.Vertical
					return material.List(a.th.Theme, &a.messageList).Layout(gtx, len(paragraphs), func(gtx layout.Context, i int) layout.Dimensions {
						p := paragraphs[i]
						if len(p) == 1 && p[0].Text == "\n" {
							return spacer(gtx, 14)(gtx)
						}

						// For each paragraph, we split spans into words for the Flow layout.
						type word struct {
							text string
							s    mail.Span
						}
						var words []word
						for _, s := range p {
							// We split by space but preserve it.
							ww := strings.SplitAfter(s.Text, " ")
							for _, wtext := range ww {
								if wtext != "" {
									words = append(words, word{text: wtext, s: s})
								}
							}
						}

						// Wrap Flow in a widget that forces the width constraint.
						return func(gtx layout.Context) layout.Dimensions {
							return Flow{}.Layout(gtx, len(words), func(gtx layout.Context, i int) layout.Dimensions {
								wd := words[i]
								if wd.s.ImageURL != "" && a.settings.RenderImages {
									a.imgMu.Lock()
									op, ok := a.imgCache[wd.s.ImageURL]
									a.imgMu.Unlock()
									if ok && op != (paint.ImageOp{}) {
										img := widget.Image{Src: op, Scale: 0.5}
										img.Fit = widget.Contain
										return func(gtx layout.Context) layout.Dimensions {
											// Limit image dimensions so they don't take over the screen.
											maxH := gtx.Dp(unit.Dp(200))
											maxW := gtx.Dp(unit.Dp(350))
											if gtx.Constraints.Max.Y > maxH {
												gtx.Constraints.Max.Y = maxH
											}
											if gtx.Constraints.Max.X > maxW {
												gtx.Constraints.Max.X = maxW
											}
											return img.Layout(gtx)
										}(gtx)
									}
									a.loadImage(wd.s.ImageURL)
									// While loading, show placeholder text
								}

								lbl := material.Label(a.th.Theme, unit.Sp(14), wd.text)
								lbl.Color = a.th.Pal.Text
								if wd.s.URL != "" {
									lbl.Color = a.th.Pal.Link
								}
								a.th.applyFont(&lbl, a.th.Fonts.Message)
								if wd.s.Bold {
									lbl.Font.Weight = font.Bold
								}
								if wd.s.Italic {
									lbl.Font.Style = font.Italic
								}
								return lbl.Layout(gtx)
							})
						}(gtx)
					})
				}),
			)
		})
}

func (a *App) messageHeaderRow(label, value string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
				lbl := material.Label(a.th.Theme, unit.Sp(11), label)
				lbl.Color = a.th.Pal.TextMuted
				a.th.applyFont(&lbl, a.th.Fonts.Message)
				return lbl.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(a.th.Theme, unit.Sp(13), value)
				lbl.Color = a.th.Pal.Text
				a.th.applyFont(&lbl, a.th.Fonts.Message)
				lbl.MaxLines = 1
				lbl.Truncator = "…"
				return lbl.Layout(gtx)
			}),
		)
	}
}

func (a *App) layoutCompose(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceEnd}.Layout(gtx,
				layout.Rigid(a.fieldRow("To:     ", &a.to)),
				layout.Rigid(spacer(gtx, 4)),
				layout.Rigid(a.fieldRow("Subject:", &a.subject)),
				layout.Rigid(spacer(gtx, 8)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return a.fieldRow("", &a.body)(gtx)
				}),
			)
		})
}

func (a *App) fieldRow(label string, ed *widget.Editor) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if label == "" {
					return layout.Dimensions{}
				}
				lbl := material.Label(a.th.Theme, unit.Sp(13), label)
				lbl.Color = a.th.Pal.TextDim
				a.th.applyFont(&lbl, a.th.Fonts.Compose)
				return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, lbl.Layout)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				e := material.Editor(a.th.Theme, ed, "")
				e.Color = a.th.Pal.Text
				e.HintColor = a.th.Pal.TextDim
				a.th.applyEditorFont(&e, a.th.Fonts.Compose)
				return e.Layout(gtx)
			}),
		)
	}
}

func (a *App) layoutHelp(gtx layout.Context) layout.Dimensions {
	help := strings.TrimSpace(`
Navigation
  j / k          down / up (next / prev in message)
  gg / G         top / bottom
  Ctrl-d/u       page down / up
  Enter / l      open message
  h              back / accounts (from list)
  Esc            back (from message / help / settings)

Actions
  e              archive
  dd             trash
  s              toggle star
  u / U          mark unread / read
  r              reply
  a              reply all
  f              forward
  c              compose
  ,              settings
  R / Ctrl-r     refresh
  /              search (Enter to run)
  n / N          next / prev (search)
  q              quit

Folders
  gi  inbox      gs  starred
  gt  sent       gT  trash

Accounts
  ga             switch to next account
  (manage with: wlmail -add / -list / -use / -rm)

Compose
  i              enter insert mode
  Esc            leave insert mode
  Ctrl-s         send
`)
	return layout.Inset{Left: unit.Dp(20), Top: unit.Dp(12)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(a.th.Theme, unit.Sp(13), help)
			a.th.applyFont(&lbl, a.th.Fonts.Global)
			lbl.Color = a.th.Pal.Text
			return lbl.Layout(gtx)
		})
}

// ---------- helpers ----------

func (a *App) coloredText(s string, c color.NRGBA, fs FontStyle) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := material.Label(a.th.Theme, unit.Sp(13), s)
		lbl.Color = c
		lbl.MaxLines = 1
		a.th.applyFont(&lbl, fs)
		return lbl.Layout(gtx)
	}
}

func centerLabel(gtx layout.Context, th *Theme, s string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(th.Theme, unit.Sp(14), s)
		l.Color = th.Pal.TextDim
		return l.Layout(gtx)
	})
}

func spacer(gtx layout.Context, dp int) layout.Widget {
	h := gtx.Dp(unit.Dp(float32(dp)))
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(0, h)}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return fmt.Sprintf("%-*s", n, s)
	}
	return s[:n-1] + "…"
}

func relTime(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d < 7*24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("Jan 02")
}

func (a *App) modeIndicator() (string, color.NRGBA) {
	switch a.mode {
	case keys.ModeInsert:
		return "INSERT", a.th.Pal.Unread
	case keys.ModeSearch:
		return "", a.th.Pal.Accent
	default:
		return "NORMAL", a.th.Pal.Accent
	}
}
