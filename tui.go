package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func tui(branches []branch, commits []commit) {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	var app *tview.Application

	// 灰色底，替代默认选中态的白底
	listStyle := tcell.StyleDefault.
		Foreground(tview.Styles.PrimaryTextColor). // 与未选中行一致，不再反色
		Background(tcell.NewHexColor(0x4a4a4a))

	headerStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	headerSelectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	// 聚焦时高亮边框与标题
	setFocusBorder := func(box *tview.Box, focused bool) {
		color := tcell.ColorGray
		if focused {
			color = tcell.ColorAqua
		}
		box.SetBorderColor(color).SetTitleColor(color)
	}

	commitList := newCommitView(listStyle)

	commitList.SetTitle(" commits ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(commitList.Box, false) })

	commitList.setLines(renderGraph(commits, computeGraph(commits)))

	// 隐藏 root，用 Local / Remote 作为分组标题。
	localNode := tview.NewTreeNode("Local").
		SetTextStyle(headerStyle).
		SetSelectedTextStyle(headerSelectedStyle)
	remoteNode := tview.NewTreeNode("Remote").
		SetTextStyle(headerStyle).
		SetSelectedTextStyle(headerSelectedStyle)
	branchRoot := tview.NewTreeNode("").
		AddChild(localNode).
		AddChild(remoteNode)

	// commit hash -> graph 行号，用于点击分支后跳转
	rowOf := make(map[string]int, len(commits))
	for i, c := range commits {
		rowOf[c.hash] = i
	}

	for _, b := range branches {
		parent := localNode
		if b.remote {
			parent = remoteNode
		}

		child := tview.NewTreeNode(b.name).
			SetSelectedTextStyle(listStyle).
			SetSelectedFunc(func() {
				if row, ok := rowOf[b.hash]; ok {
					commitList.setCurrentItem(row)
				}
				// app.SetFocus(commitList)
			})

		parent.AddChild(child)
	}

	if len(remoteNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(remoteNode)
	}

	branchList := tview.NewTreeView().
		SetRoot(branchRoot).
		SetTopLevel(1).
		SetGraphics(false)

	branchList.SetTitle(" branches ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(branchList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(branchList.Box, false) })

	// 默认选中第一个分支，而不是分组标题
	if children := localNode.GetChildren(); len(children) > 0 {
		branchList.SetCurrentNode(children[0])
	} else if children := remoteNode.GetChildren(); len(children) > 0 {
		branchList.SetCurrentNode(children[0])
	}

	// TreeView 在按下鼠标后会重绘，并把视口外的选中项拉回可见范围，
	// 导致随后的点击按新的 offsetY 命中错误的行。这里跳过 down 的默认处理，
	// 避免这次重绘；点击仍由 TreeView 默认逻辑处理。
	branchList.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if !branchList.InRect(event.Position()) {
			return action, event
		}
		switch action {
		case tview.MouseLeftDown:
			if !branchList.HasFocus() {
				app.SetFocus(branchList)
			}
			return tview.MouseMove, event
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !branchList.HasFocus() {
				app.SetFocus(branchList)
			}
		}
		return action, event
	})

	root := tview.NewFlex().
		AddItem(branchList, 40, 0, false).
		AddItem(commitList, 0, 1, true)

	// SetRoot 只会对 commitList 触发 focus 回调，branchList 需手动初始化为未聚焦
	setFocusBorder(commitList.Box, true)
	setFocusBorder(branchList.Box, false)

	app = tview.NewApplication().
		EnableMouse(true).
		SetRoot(root, true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q'):
			app.Stop()
			return nil
		case event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab:
			// 只有两个面板，Tab 与 Shift-Tab 都是互相切换
			if commitList.HasFocus() {
				app.SetFocus(branchList)
			} else {
				app.SetFocus(commitList)
			}
			return nil
		}
		return event
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

const (
	colorReset = "[-::-]" // 同时重置前景色和属性，否则 refs 的粗体会延续到 subject
	colorHash  = "[yellow]"
	colorRefs  = "[aqua::b]"
)

var laneColors = []string{
	"[green]",
	"[yellow]",
	"[blue]",
	"[fuchsia]",
	"[aqua]",
	"[red]",
}

func laneColor(lane int) string {
	return laneColors[lane%len(laneColors)]
}

type cell struct {
	up, down, left, right bool
	vColor, hColor        string // 竖线颜色 / 横线颜色
}

// glyphs 返回 cell 的线条字符和其右侧的连接字符，规则基本与 lazygit 一致：横线穿过竖线时竖线优先；
// 边汇入一条已存在的 lane 时用 ┤/├ 表示汇合点
func (c cell) glyphs() (line, conn string) {
	conn = " "
	if c.right {
		conn = "─"
	}
	switch {
	case c.up && c.down && c.left && !c.right:
		return "┤", conn
	case c.up && c.down && c.right && !c.left:
		return "├", conn
	case c.up && c.down:
		return "│", conn
	case c.up && c.left && c.right:
		return "┴", conn
	case c.up && c.left:
		return "╯", conn
	case c.up && c.right:
		return "╰", conn
	case c.up:
		return "╵", conn
	case c.down && c.left && c.right:
		return "┬", conn
	case c.down && c.left:
		return "╮", conn
	case c.down && c.right:
		return "╭", conn
	case c.down:
		return "╷", conn
	case c.left:
		return "─", conn
	case c.right:
		return "╶", conn
	default:
		return " ", conn
	}
}

func renderGraph(commits []commit, graph graph) []string {
	lines := make([]string, 0, len(graph.rows))

	for r, row := range graph.rows {
		var out strings.Builder
		cells := make([]cell, graph.laneCount)

		inRange := func(i int) bool {
			return i >= 0 && i < len(cells)
		}
		// 在节点 node 与 lane end 之间画一条边；isTop 表示边从上方进入，否则向下方离开
		drawEdge := func(end, node int, isTop bool) {
			color := laneColor(end)
			if inRange(end) {
				if isTop {
					cells[end].up = true
				} else {
					cells[end].down = true
				}
				cells[end].vColor = color
			}
			if end == node {
				return
			}
			lo, hi := min(end, node), max(end, node)
			for k := lo; k <= hi; k++ {
				if !inRange(k) {
					continue
				}
				if k > lo {
					cells[k].left = true
				}
				if k < hi {
					cells[k].right = true
				}
				cells[k].hColor = color
			}
		}

		for _, e := range row.top {
			drawEdge(e[0], e[1], true)
		}
		for _, e := range row.bottom {
			drawEdge(e[1], e[0], false)
		}

		commit := commits[r]
		// lane 数超过 MAX_LANES 时节点可能落在可视范围外，钳到最后一列保证每行都有节点
		nodeCol := min(row.nodeCol, len(cells)-1)
		for i, c := range cells {
			var char, conn string
			charColor := c.vColor
			if i == nodeCol {
				char = "◯"
				if len(commit.parents) > 1 {
					char = "◎"
				}
				charColor = laneColor(i)
				conn = " "
				if c.right {
					conn = "─"
				}
			} else {
				char, conn = c.glyphs()
				if !c.up && !c.down {
					charColor = c.hColor
				}
			}
			fmt.Fprintf(&out, "%s%s%s%s%s", charColor, char, c.hColor, conn, colorReset)
		}

		hash := commit.hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		fmt.Fprintf(&out, " %s%s%s", colorHash, hash, colorReset)
		if commit.refs != "" {
			fmt.Fprintf(&out, " %s(%s)%s", colorRefs, tview.Escape(commit.refs), colorReset)
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.subject))
		lines = append(lines, out.String())
	}

	return lines
}

// commitView 把视口偏移和选中项分开，使滚轮只滚动内容。
// tview.List 的 Draw 会强制让选中项保持可见，无法做到这一点。
type commitView struct {
	*tview.Box

	lines    []string
	offset   int
	selected int

	selectedStyle tcell.Style
}

func newCommitView(selectedStyle tcell.Style) *commitView {
	return &commitView{
		Box:           tview.NewBox(),
		selectedStyle: selectedStyle,
	}
}

func (v *commitView) Draw(screen tcell.Screen) {
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
		if index == v.selected {
			for bx := 0; bx < width; bx++ {
				screen.SetContent(x+bx, y+row, ' ', nil, v.selectedStyle)
			}
		}
		tview.Print(screen, v.lines[index], x, y+row, width, tview.AlignLeft, tview.Styles.PrimaryTextColor)
	}
}

func (v *commitView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
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

func (v *commitView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
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
					v.selected = index
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

func (v *commitView) setLines(lines []string) {
	v.lines = lines
}

func (v *commitView) setCurrentItem(index int) {
	if len(v.lines) == 0 {
		return
	}
	v.selected = min(max(index, 0), len(v.lines)-1)
	v.ensureVisible()
}

func (v *commitView) ensureVisible() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return
	}
	if v.selected < v.offset {
		v.offset = v.selected
	} else if v.selected >= v.offset+height {
		v.offset = v.selected - height + 1
	}
	v.clampOffset()
}

func (v *commitView) clampOffset() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		return
	}
	v.offset = min(max(v.offset, 0), max(0, len(v.lines)-height))
}

func (v *commitView) move(delta int) {
	index := v.selected + delta
	if delta == 1 && index >= len(v.lines) {
		index = 0
	} else if delta == -1 && index < 0 {
		index = len(v.lines) - 1
	}
	v.setCurrentItem(index)
}
