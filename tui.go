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
		case event.Key() == tcell.KeyTab:
			switch {
			case c.branchList.HasFocus():
				app.SetFocus(c.commitList)
			case c.commitList.HasFocus():
				app.SetFocus(c.fileList)
			case c.fileList.HasFocus():
				app.SetFocus(c.fileDiff)
			default:
				app.SetFocus(c.branchList)
			}
			return nil
		case event.Key() == tcell.KeyBacktab:
			switch {
			case c.branchList.HasFocus():
				app.SetFocus(c.fileDiff)
			case c.commitList.HasFocus():
				app.SetFocus(c.branchList)
			case c.fileList.HasFocus():
				app.SetFocus(c.commitList)
			default:
				app.SetFocus(c.fileList)
			}
			return nil
		}
		return event
	})

	c.app = app
	c.refresh()

	return app.Run()
}

func (c *client) setupUI() tview.Primitive {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	// 选中态用灰底代替 tview 默认的白底反色；前景色与未选中行一致
	selectedStyle := tcell.StyleDefault.
		Foreground(tview.Styles.PrimaryTextColor).
		Background(tcell.NewHexColor(0x4a4a4a))

	headerStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	headerSelectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	setFocusBorder := func(box *tview.Box, focused bool) {
		color := tcell.ColorGray
		if focused {
			color = tcell.ColorAqua
		}
		box.SetBorderColor(color).SetTitleColor(color)
	}

	setFolderExpanded := func(node *tview.TreeNode, expanded bool) {
		node.SetExpanded(expanded)
		if name, ok := node.GetReference().(string); ok {
			node.SetText(folderLabel(name, expanded))
		}
	}

	commitList := newLaneView(selectedStyle)
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

	fileRoot := buildFileTree(nil, selectedStyle)

	fileList := tview.NewTreeView().
		SetRoot(fileRoot).
		SetTopLevel(1).
		SetGraphics(false)

	fileList.SetTitle(" file changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(fileList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(fileList.Box, false) })

	fileList.SetSelectedFunc(func(node *tview.TreeNode) {
		if len(node.GetChildren()) == 0 {
			c.showFileDiff(node)
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	fileList.SetChangedFunc(func(node *tview.TreeNode) {
		c.showFileDiff(node)
	})

	fileDiff := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetText("[gray]select a file[-]")

	fileDiff.SetTitle(" file diff ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(fileDiff.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(fileDiff.Box, false) })

	// 滚轮滚动 diff 时同步聚焦，边框状态保持一致
	fileDiff.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if !fileDiff.InRect(event.Position()) {
			return action, event
		}
		switch action {
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !fileDiff.HasFocus() {
				c.app.SetFocus(fileDiff)
			}
		}
		return action, event
	})

	commitDetail := tview.NewFlex().
		AddItem(fileList, 40, 1, false).
		AddItem(fileDiff, 0, 1, false)

	commitPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(commitList, 20, 0, true).
		AddItem(commitInfo, 4, 0, false).
		AddItem(commitDetail, 0, 1, false)

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
			SetSelectedTextStyle(selectedStyle).
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

	branchHead.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick || !branchHead.InRect(event.Position()) {
			return action, event
		}
		if row, ok := rowOf[c.headHash]; ok {
			commitList.setCurrentItem(row)
		}
		if headNode != nil {
			branchList.SetCurrentNode(headNode)
			c.app.SetFocus(branchList)
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
	setFocusBorder(fileList.Box, false)
	setFocusBorder(fileDiff.Box, false)

	c.branchList = branchList
	c.commitList = commitList
	c.commitInfo = commitInfo
	c.fileList = fileList
	c.fileDiff = fileDiff
	c.selectedStyle = selectedStyle

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
		c.refreshFiles(commit)
	}
}

func (c *client) refreshFiles(commit commit) {
	c.currentCommit = commit
	if commit.hash == c.filesHash {
		return
	}

	changes, ok := c.fileCache[commit.hash]
	var err error
	if !ok {
		changes, err = gitCommitFiles(c.repo, commit)
		if err == nil {
			if c.fileCache == nil {
				c.fileCache = make(map[string][]fileChange)
			}
			c.fileCache[commit.hash] = changes
		}
	}

	title := " file changes "
	var fileRoot *tview.TreeNode
	switch {
	case err != nil:
		fileRoot = tview.NewTreeNode("")
		fileRoot.AddChild(tview.NewTreeNode("[red]error[-] " + tview.Escape(err.Error())))
	case len(changes) == 0:
		title = " file changes (0) "
		fileRoot = tview.NewTreeNode("")
		fileRoot.AddChild(tview.NewTreeNode("[gray](no changes)[-]"))
	default:
		title = fmt.Sprintf(" file changes (%d) ", len(changes))
		fileRoot = buildFileTree(changes, c.selectedStyle)
	}
	if err == nil {
		c.filesHash = commit.hash
	} else {
		c.filesHash = "" // 下次刷新/重新选中时重试
	}
	c.fileList.SetTitle(title)
	c.fileList.SetRoot(fileRoot)

	switch {
	case err != nil:
		c.clearFileDiff("failed to load file changes")
	case len(changes) == 0:
		c.clearFileDiff("no changes")
	default:
		first := firstFileNode(fileRoot)
		if first == nil {
			c.clearFileDiff("select a file")
			return
		}
		c.fileList.SetCurrentNode(first)
		c.showFileDiff(first)
	}
}

func (c *client) showFileDiff(node *tview.TreeNode) {
	change, ok := node.GetReference().(fileChange)
	if !ok {
		// 目录节点提示选择文件；错误/无变更占位节点保留 refreshFiles 设置的内容
		if _, isFolder := node.GetReference().(string); isFolder {
			c.clearFileDiff("select a file")
		}
		return
	}

	if c.diffCache == nil {
		c.diffCache = make(map[string]string)
	}
	key := c.currentCommit.hash + "\x00" + change.path
	// TreeView 在 Draw 中可能对同一节点再次触发 changed（鼠标点选时尤其如此）。
	// 若在 diff 已绘制后再 SetText，会清空 TextView 的行索引，下一次滚轮就会
	// 误判为已到底并打开 trackEnd，导致视口直接跳到末尾。同一份 diff 不重复渲染。
	if key == c.diffShown {
		return
	}
	c.diffShown = key
	if text, ok := c.diffCache[key]; ok {
		c.setFileDiff(change, text)
		return
	}

	output, err := gitFileDiff(c.repo, c.currentCommit, change)
	var text string
	switch {
	case err != nil && output == "":
		text = "[red]error[-] " + tview.Escape(err.Error())
	case err != nil:
		// git-delta 默认可用；万一执行失败，回退到原始 diff 的简单着色
		text = buildPlainDiff(output)
	default:
		text = buildDeltaDiff(output)
	}
	if text == "" {
		text = "[gray](no diff)[-]"
	}

	c.diffCache[key] = text
	c.setFileDiff(change, text)
}

func (c *client) setFileDiff(change fileChange, text string) {
	c.fileDiff.SetTitle(" file diff: " + tview.Escape(change.path) + " ")
	c.fileDiff.SetText(text)
	c.fileDiff.ScrollToBeginning()
}

func (c *client) clearFileDiff(message string) {
	c.diffShown = ""
	c.fileDiff.SetTitle(" file diff ")
	c.fileDiff.SetText("[gray]" + tview.Escape(message) + "[-]")
	c.fileDiff.ScrollToBeginning()
}
