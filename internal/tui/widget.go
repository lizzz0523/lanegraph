package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// laneView 把视口偏移和选中项分开，使滚轮只滚动内容。
// tview.List 的 Draw 会强制让选中项保持可见，无法做到这一点。
type laneView struct {
	*tview.Box

	lines   []string
	offset  int
	marked  int
	current int

	selected       func()
	selectedStyle  tcell.Style
	highlight      func(index int)
	highlightStyle tcell.Style
}

func newLaneView(selectedStyle, highlightStyle tcell.Style) *laneView {
	return &laneView{
		Box:            tview.NewBox(),
		marked:         -1,
		selectedStyle:  selectedStyle,
		highlightStyle: highlightStyle,
	}
}

func (v *laneView) Draw(screen tcell.Screen) {
	v.Box.DrawForSubclass(screen, v)

	x, y, width, height := v.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}
	v.clampOffset()

	for row := 0; row < height; row++ {
		index := v.offset + row
		if index >= len(v.lines) {
			break
		}
		style := v.selectedStyle
		if index == v.marked && index != v.current {
			style = v.highlightStyle
		}
		if index == v.current || index == v.marked {
			for bx := 0; bx < width; bx++ {
				screen.SetContent(x+bx, y+row, ' ', nil, style)
			}
		}
		tview.Print(screen, v.lines[index], x, y+row, width, tview.AlignLeft, tview.Styles.PrimaryTextColor)
	}
}

func (v *laneView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return v.Box.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if len(v.lines) == 0 {
			return
		}
		_, _, _, height := v.GetInnerRect()
		switch event.Key() {
		case tcell.KeyUp:
			v.move(-1)
		case tcell.KeyDown:
			v.move(1)
		case tcell.KeyPgUp:
			v.move(-height)
		case tcell.KeyPgDn:
			v.move(height)
		case tcell.KeyHome:
			v.setCurrentItem(0)
		case tcell.KeyEnd:
			v.setCurrentItem(len(v.lines) - 1)
		}
	})
}

func (v *laneView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return v.Box.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		if !v.InRect(event.Position()) {
			return false, nil
		}
		switch action {
		case tview.MouseLeftClick:
			setFocus(v)
			x, y, width, height := v.GetInnerRect()
			mx, my := event.Position()
			if mx >= x && mx < x+width && my >= y && my < y+height {
				if index := v.offset + my - y; index < len(v.lines) {
					// Ctrl+点击只设置对比基准，不移动当前选中项。
					if event.Modifiers()&tcell.ModCtrl != 0 {
						if v.highlight != nil {
							v.highlight(index)
						}
					} else {
						v.current = index
						v.selected()
					}
				}
			}
			consumed = true
		case tview.MouseScrollUp:
			setFocus(v)
			v.offset--
			v.clampOffset()
			consumed = true
		case tview.MouseScrollDown:
			setFocus(v)
			v.offset++
			v.clampOffset()
			consumed = true
		}
		return
	})
}

func (v *laneView) setLines(lines []string) {
	v.lines = lines
	v.current = min(v.current, max(0, len(lines)-1))
}

// setMarkedItem 标记对比基准所在行；index 越界时清除标记。
func (v *laneView) setMarkedItem(index int) {
	if index < 0 || index >= len(v.lines) {
		v.marked = -1
		return
	}
	v.marked = index
}

func (v *laneView) setCurrentItem(index int) {
	if len(v.lines) == 0 {
		return
	}
	v.current = min(max(index, 0), len(v.lines)-1)
	v.ensureVisible()
	v.selected()
}

func (v *laneView) setSelectedFunc(handler func()) {
	v.selected = handler
}

func (v *laneView) setHighlightFunc(handler func(index int)) {
	v.highlight = handler
}

func (v *laneView) currentItem() int {
	return v.current
}

func (v *laneView) ensureVisible() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return
	}
	if v.current < v.offset {
		v.offset = v.current
	} else if v.current >= v.offset+height {
		v.offset = v.current - height + 1
	}
	v.clampOffset()
}

func (v *laneView) clampOffset() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return
	}
	v.offset = min(max(v.offset, 0), max(0, len(v.lines)-height))
}

func (v *laneView) move(delta int) {
	index := v.current + delta
	if delta == 1 && index >= len(v.lines) {
		index = 0
	} else if delta == -1 && index < 0 {
		index = len(v.lines) - 1
	}
	v.setCurrentItem(index)
}
