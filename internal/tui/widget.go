package tui

import (
	"sort"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// listView 把视口偏移和选中项分开，使滚轮只滚动内容。
// tview.List 的 Draw 会强制让选中项保持可见，无法做到这一点。
// 每个 item 可以包含多行，选中和高亮按 item 生效。
type listView struct {
	*tview.Box

	items  [][]string
	total  int   // 所有 item 的总行数
	starts []int // 每个 item 第一行的绝对行号

	offset  int
	marked  int
	current int

	selected       func()
	selectedStyle  tcell.Style
	highlight      func(index int)
	highlightStyle tcell.Style
}

func newListView(selectedStyle, highlightStyle tcell.Style) *listView {
	return &listView{
		Box:            tview.NewBox(),
		marked:         -1,
		selectedStyle:  selectedStyle,
		highlightStyle: highlightStyle,
	}
}

func (v *listView) Draw(screen tcell.Screen) {
	v.Box.DrawForSubclass(screen, v)

	x, y, width, height := v.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}
	v.clampOffset()

	first := sort.Search(len(v.items), func(i int) bool {
		return v.starts[i]+len(v.items[i]) > v.offset
	})
	for i := first; i < len(v.items); i++ {
		for j, line := range v.items[i] {
			row := v.starts[i] + j - v.offset
			if row < 0 {
				continue
			}
			if row >= height {
				return
			}
			style := v.selectedStyle
			if i == v.marked && i != v.current {
				style = v.highlightStyle
			}
			if i == v.current || i == v.marked {
				for bx := 0; bx < width; bx++ {
					screen.SetContent(x+bx, y+row, ' ', nil, style)
				}
			}
			tview.Print(screen, line, x, y+row, width, tview.AlignLeft, tview.Styles.PrimaryTextColor)
		}
	}
}

func (v *listView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return v.Box.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if len(v.items) == 0 {
			return
		}
		switch event.Key() {
		case tcell.KeyUp:
			v.move(-1)
		case tcell.KeyDown:
			v.move(1)
		case tcell.KeyPgUp:
			v.move(-v.pageItems())
		case tcell.KeyPgDn:
			v.move(v.pageItems())
		case tcell.KeyHome:
			v.setCurrentItem(0)
		case tcell.KeyEnd:
			v.setCurrentItem(len(v.items) - 1)
		}
	})
}

func (v *listView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
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
				if index := v.itemAtLine(v.offset + my - y); index >= 0 {
					// Ctrl+点击只设置对比基准，不移动当前选中项。
					if event.Modifiers()&tcell.ModCtrl != 0 {
						if v.highlight != nil {
							v.highlight(index)
						}
					} else {
						v.current = index
						if v.selected != nil {
							v.selected()
						}
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

// setItems 替换全部 item，并重新计算每个 item 的起始行号。
func (v *listView) setItems(items [][]string) {
	v.items = items
	v.starts = make([]int, len(items))
	v.total = 0
	for i, item := range items {
		v.starts[i] = v.total
		v.total += len(item)
	}

	v.current = min(max(v.current, 0), max(0, len(items)-1))
	if v.marked >= len(items) {
		v.marked = -1
	}
	v.clampOffset()
}

// setLines 是单行 item 的快捷方式，供 commit 列表使用。
func (v *listView) setLines(lines []string) {
	items := make([][]string, len(lines))
	for i, line := range lines {
		items[i] = []string{line}
	}
	v.setItems(items)
}

// setMarkedItem 标记对比基准所在 item；index 越界时清除标记。
func (v *listView) setMarkedItem(index int) {
	if index < 0 || index >= len(v.items) {
		v.marked = -1
		return
	}
	v.marked = index
}

func (v *listView) setCurrentItem(index int) {
	if len(v.items) == 0 {
		return
	}
	v.current = min(max(index, 0), len(v.items)-1)
	v.ensureVisible()
	if v.selected != nil {
		v.selected()
	}
}

func (v *listView) setSelectedFunc(handler func()) {
	v.selected = handler
}

func (v *listView) setHighlightFunc(handler func(index int)) {
	v.highlight = handler
}

func (v *listView) currentItem() int {
	return v.current
}

func (v *listView) ensureVisible() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 || v.current < 0 || v.current >= len(v.items) {
		return
	}
	start := v.starts[v.current]
	end := start + len(v.items[v.current]) - 1
	if start < v.offset {
		v.offset = start
	} else if end >= v.offset+height {
		v.offset = end - height + 1
	}
	v.clampOffset()
}

func (v *listView) clampOffset() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return
	}
	v.offset = min(max(v.offset, 0), max(0, v.total-height))
}

// pageItems 返回当前视口大致能容纳的 item 数量，用于 PgUp/PgDn。
func (v *listView) pageItems() int {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return 1
	}
	lines, count := 0, 0
	for i := v.current; i < len(v.items); i++ {
		lines += len(v.items[i])
		if lines > height {
			break
		}
		count++
	}
	return max(1, count)
}

// itemAtLine 返回绝对行号 line 所属的 item；落在空白处时返回 -1。
func (v *listView) itemAtLine(line int) int {
	if line < 0 || line >= v.total {
		return -1
	}
	index := sort.Search(len(v.starts), func(i int) bool {
		return v.starts[i] > line
	}) - 1
	if index >= 0 && line < v.starts[index]+len(v.items[index]) {
		return index
	}
	return -1
}

func (v *listView) move(delta int) {
	index := v.current + delta
	if delta == 1 && index >= len(v.items) {
		index = 0
	} else if delta == -1 && index < 0 {
		index = len(v.items) - 1
	}
	v.setCurrentItem(index)
}
