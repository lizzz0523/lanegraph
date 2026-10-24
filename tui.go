package main

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

func (c *client) run() error {
	app := tview.NewApplication().
		SetRoot(c.setupUI(), true).
		EnableMouse(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q'):
			app.Stop()
			return nil
		case event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab:
			// 只有两个面板，Tab 与 Shift-Tab 都是互相切换
			if c.commitList.HasFocus() {
				app.SetFocus(c.branchList)
			} else {
				app.SetFocus(c.commitList)
			}
			return nil
		}
		return event
	})

	c.app = app

	return app.Run()
}

func (c *client) setupUI() tview.Primitive {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

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

	commitList := newLaneView(listStyle)
	commitList.setSelectedFunc(c.refresh)

	commitList.SetTitle(" lanegraph ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(commitList.Box, false) })

	commitList.setLines(renderGraph(c.commits, computeGraph(c.commits)))

	commitInfo := tview.NewTextView().
		SetWrap(false).
		SetDynamicColors(true)

	commitInfo.SetTitle(" commit ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
			x, y, width, height := commitInfo.GetInnerRect()
			if width != c.infoWidth {
				c.infoWidth = width
				c.refresh()
			}
			return x, y, width, height
		})

	fileList := tview.NewTreeView()

	fileList.SetTitle(" changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true)

	fileDiff := newDiffView()

	fileDiff.SetTitle(" diffs ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true)

	commitDetail := tview.NewFlex().
		AddItem(fileList, 40, 1, false).
		AddItem(fileDiff, 0, 1, false)

	commitPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(commitList, 0, 1, true).
		AddItem(commitInfo, 4, 0, false).
		AddItem(commitDetail, 20, 0, false)

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
	rowOf := make(map[string]int, len(c.commits))
	for i, c := range c.commits {
		rowOf[c.hash] = i
	}

	var headNode *tview.TreeNode

	for _, b := range c.branches {
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
			})

		if !b.remote && b.name == c.headRef {
			headNode = child
		}

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
				c.app.SetFocus(branchList)
			}
			return tview.MouseMove, event
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !branchList.HasFocus() {
				c.app.SetFocus(branchList)
			}
		}
		return action, event
	})

	branchHead := tview.NewTextView().
		SetText(c.headRef)

	branchHead.SetTitle(" "+c.repoName+" ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	// 点击 branch head 定位到 HEAD 提交
	branchHead.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick || !branchHead.InRect(event.Position()) {
			return action, event
		}
		if row, ok := rowOf[c.headHash]; ok {
			commitList.setCurrentItem(row)
			c.app.SetFocus(commitList)
		}
		if headNode != nil {
			branchList.SetCurrentNode(headNode)
		}
		return tview.MouseConsumed, nil
	})

	branchPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(branchHead, 3, 0, false).
		AddItem(branchList, 0, 1, false)

	root := tview.NewFlex().
		AddItem(branchPanel, 40, 0, false).
		AddItem(commitPanel, 0, 1, true)

	// SetRoot 只会对 commitList 触发 focus 回调，branchList 需手动初始化为未聚焦
	setFocusBorder(branchList.Box, false)
	setFocusBorder(commitList.Box, true)

	c.branchList = branchList
	c.commitList = commitList
	c.commitInfo = commitInfo
	c.fileList = fileList
	c.fileDiff = fileDiff

	return root
}

func (c *client) refresh() {
	if row := c.commitList.currentItem(); row < len(c.commits) {
		commit := c.commits[row]
		text := fmt.Sprintf("[aqua]%v[-] [gray]%v[-]  [::b]%v[-::-]", tview.Escape(commit.author), commit.date.Format("2006-01-02 15:04:05"), tview.Escape(commit.subject))
		if body := strings.TrimLeft(commit.body, "\r\n"); body != "" {
			first, _, more := strings.Cut(body, "\n")
			if c.infoWidth > 4 {
				if truncate := runewidth.Truncate(first, c.infoWidth-4, ""); truncate != first {
					first, more = truncate, true
				}
			}
			text += "\n" + tview.Escape(first)
			if more {
				text += " [gray]...[-]"
			}
		}
		c.commitInfo.SetText(text)
	}
}

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

func (v *laneView) move(delta int) {
	index := v.current + delta
	if delta == 1 && index >= len(v.lines) {
		index = 0
	} else if delta == -1 && index < 0 {
		index = len(v.lines) - 1
	}
	v.setCurrentItem(index)
}

type diffView struct {
	*tview.Box
}

func newDiffView() *diffView {
	return &diffView{
		Box: tview.NewBox(),
	}
}
