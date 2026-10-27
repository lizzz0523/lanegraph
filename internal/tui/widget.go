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
	current int

	selected      func()
	selectedStyle tcell.Style
}

func newLaneView(selectedStyle tcell.Style) *laneView {
	return &laneView{
		Box:           tview.NewBox(),
		selectedStyle: selectedStyle,
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
		if index == v.current {
			for bx := 0; bx < width; bx++ {
				screen.SetContent(x+bx, y+row, ' ', nil, v.selectedStyle)
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

func (v *laneView) move(delta int) {
	index := v.current + delta
	if delta == 1 && index >= len(v.lines) {
		index = 0
	} else if delta == -1 && index < 0 {
		index = len(v.lines) - 1
	}
	v.setCurrentItem(index)
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
					v.current = index
					v.selected()
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
