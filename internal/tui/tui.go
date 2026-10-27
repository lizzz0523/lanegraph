package tui

import (
	"context"
	"fmt"
	"strings"

	"lanegraph/internal/async"
	"lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

// 主面板 tab，同时用作 Pages 的页名。
const (
	tabChange = "change"
	tabCommit = "commit"
)

type TUI struct {
	*core.State
	app            *tview.Application
	mainPanels     *tview.Pages
	changeTab      *tview.Button
	commitTab      *tview.Button
	branchList     *tview.TreeView
	commitPanel    *tview.Flex
	changePanel    *tview.Flex
	commitList     *laneView
	commitInfo     *tview.TextView
	fileList       *tview.TreeView
	fileDiff       *tview.TextView
	activeTab      string // 当前显示的主面板 tab
	infoWidth      int
	statusBar      *tview.TextView
	statusBusy     map[string]string // statusBar 当前展示的 loading 项，key 为槽位
	statusMsg      string            // 最近一次操作结果，loading 结束后保留
	tabStyle       tcell.Style
	tabActiveStyle tcell.Style
	selectedStyle  tcell.Style

	files        *async.Loader[[]core.FileChange, filesRequest]
	filesLoading bool // 文件列表加载中：保留旧树并忽略交互
	diff         *async.Loader[string, diffRequest]
}

type filesRequest struct {
	commit core.Commit
}

type diffRequest struct {
	commit core.Commit
	change core.FileChange
}

func New(st *core.State) *TUI {
	return &TUI{State: st}
}

func (t *TUI) Run() error {
	app := tview.NewApplication().
		SetRoot(t.setupUI(), true).
		EnableMouse(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q'):
			app.Stop()
			return nil
		case event.Key() == tcell.KeyTab:
			switch {
			case t.activeTab == tabChange:
				app.SetFocus(t.branchList)
			case t.branchList.HasFocus():
				app.SetFocus(t.commitList)
			case t.commitList.HasFocus():
				app.SetFocus(t.fileList)
			case t.fileList.HasFocus():
				app.SetFocus(t.fileDiff)
			default:
				app.SetFocus(t.branchList)
			}
			return nil
		case event.Key() == tcell.KeyBacktab:
			switch {
			case t.activeTab == tabChange:
				app.SetFocus(t.branchList)
			case t.branchList.HasFocus():
				app.SetFocus(t.fileDiff)
			case t.commitList.HasFocus():
				app.SetFocus(t.branchList)
			case t.fileList.HasFocus():
				app.SetFocus(t.commitList)
			default:
				app.SetFocus(t.fileList)
			}
			return nil
		}
		return event
	})

	t.app = app

	t.initAsync()
	defer t.files.Close()
	defer t.diff.Close()

	t.refreshCommit(true)

	return app.Run()
}

func (t *TUI) initAsync() {
	post := func(f func()) { t.app.QueueUpdateDraw(f) }

	t.files = async.New(async.Config[[]core.FileChange, filesRequest]{
		Key: func(req filesRequest) string { return req.commit.Hash },
		Load: func(ctx context.Context, req filesRequest) ([]core.FileChange, error) {
			return t.Files(ctx, req.commit)
		},
		Post:      post,
		OnLoading: t.showFilesLoading,
		OnResult:  t.showFiles,
	})

	t.diff = async.New(async.Config[string, diffRequest]{
		Key: func(req diffRequest) string {
			return req.commit.Hash + "\x00" + req.change.Path
		},
		Load: func(ctx context.Context, req diffRequest) (string, error) {
			output, err := t.Diff(ctx, req.commit, req.change)
			switch {
			case err != nil && output == "":
				return "", err
			case err != nil:
				return buildPlainDiff(output), nil
			default:
				return buildDeltaDiff(output), nil
			}
		},
		Post:      post,
		OnLoading: t.showDiffLoading,
		OnResult:  t.showDiff,
	})
}

func (t *TUI) setupUI() tview.Primitive {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	defaultStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tview.Styles.PrimitiveBackgroundColor)

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
	commitList.setSelectedFunc(func() { t.refreshCommit(false) })

	commitList.SetTitle(" all commits ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(commitList.Box, false) })

	commitList.setLines(renderGraph(t.Commits, core.ComputeGraph(t.Commits)))

	commitInfo := tview.NewTextView().
		SetWrap(false).
		SetDynamicColors(true)

	commitInfo.SetTitle(" commit ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
			x, y, width, height := commitInfo.GetInnerRect()
			if width != t.infoWidth {
				t.infoWidth = width
				t.refreshCommit(true)
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
		if t.filesLoading {
			return
		}
		if len(node.GetChildren()) == 0 {
			t.showFileDiff(node)
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	fileList.SetChangedFunc(func(node *tview.TreeNode) {
		t.showFileDiff(node)
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
				t.app.SetFocus(fileDiff)
			}
		}
		return action, event
	})

	commitDetail := tview.NewFlex().
		AddItem(fileList, 40, 1, false).
		AddItem(fileDiff, 0, 1, false)

	commitPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(commitList, 18, 0, true).
		AddItem(commitInfo, 4, 0, false).
		AddItem(commitDetail, 0, 1, false)

	changeList := tview.NewBox().
		SetTitle(" local changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	changeDiff := tview.NewBox().
		SetTitle(" file diff ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	changePanel := tview.NewFlex().
		AddItem(changeList, 40, 0, false).
		AddItem(changeDiff, 0, 1, false)

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
	rowOf := make(map[string]int, len(t.Commits))
	for i, c := range t.Commits {
		rowOf[c.Hash] = i
	}

	var headNode *tview.TreeNode

	for _, b := range t.Branches {
		parent := localNode
		if b.Remote {
			parent = remoteNode
		}

		child := tview.NewTreeNode(b.Name).
			SetSelectedTextStyle(selectedStyle).
			SetSelectedFunc(func() {
				if row, ok := rowOf[b.Hash]; ok {
					commitList.setCurrentItem(row)
				}
			})

		if !b.Remote && b.Name == t.HeadRef {
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
				t.app.SetFocus(branchList)
			}
			return tview.MouseMove, event
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !branchList.HasFocus() {
				t.app.SetFocus(branchList)
			}
		}
		return action, event
	})

	branchHead := tview.NewTextView().
		SetText(t.HeadRef)

	branchHead.SetTitle(" "+t.RepoName+" ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	branchHead.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick || !branchHead.InRect(event.Position()) {
			return action, event
		}
		if row, ok := rowOf[t.HeadHash]; ok {
			commitList.setCurrentItem(row)
		}
		if headNode != nil {
			branchList.SetCurrentNode(headNode)
			t.app.SetFocus(branchList)
		}
		return tview.MouseConsumed, nil
	})

	changeTab := tview.NewButton("Local Changes")
	commitTab := tview.NewButton("All Commits")

	// 左键按下也要吞掉，否则 Button 会自己 setFocus，抢走列表焦点。
	bindTab := func(button *tview.Button, tag string) {
		button.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
			switch action {
			case tview.MouseLeftDown, tview.MouseLeftClick:
				if !button.InRect(event.Position()) {
					return action, event
				}
				if action == tview.MouseLeftClick {
					t.switchTab(tag)
				}
				return tview.MouseConsumed, nil
			}
			return action, event
		})
	}

	bindTab(changeTab, tabChange)
	bindTab(commitTab, tabCommit)

	mainTabs := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(changeTab, 1, 0, false).
		AddItem(commitTab, 1, 0, false)

	branchPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(branchHead, 3, 0, false).
		AddItem(mainTabs, 2, 0, false).
		AddItem(branchList, 0, 1, false)

	// Pages 会先把整块区域刷成背景色再画当前页，
	// 所以切换时不会残留上一个面板的像素。
	mainPanels := tview.NewPages().
		AddPage(tabChange, changePanel, true, false).
		AddPage(tabCommit, commitPanel, true, true)

	mainContent := tview.NewFlex().
		AddItem(branchPanel, 30, 0, false).
		AddItem(mainPanels, 0, 1, true)

	statusBar := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false)

	statusBar.SetBorderPadding(0, 0, 1, 1)

	root := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(mainContent, 0, 1, true).
		AddItem(statusBar, 1, 0, false)

	// SetRoot 只会对 commitList 触发 focus 回调，branchList 需手动初始化为未聚焦
	setFocusBorder(branchList.Box, false)
	setFocusBorder(commitList.Box, true)
	setFocusBorder(fileList.Box, false)
	setFocusBorder(fileDiff.Box, false)
	setFocusBorder(changePanel.Box, false)

	t.branchList = branchList
	t.changeTab = changeTab
	t.commitTab = commitTab
	t.mainPanels = mainPanels
	t.commitPanel = commitPanel
	t.changePanel = changePanel
	t.commitList = commitList
	t.commitInfo = commitInfo
	t.fileList = fileList
	t.fileDiff = fileDiff
	t.activeTab = tabCommit
	t.statusBar = statusBar
	t.statusBusy = make(map[string]string)
	t.tabStyle = defaultStyle
	t.tabActiveStyle = selectedStyle
	t.selectedStyle = selectedStyle
	t.applyTabStyles()

	return root
}

func (t *TUI) switchTab(tag string) {
	if tag == t.activeTab {
		return
	}

	// SwitchToPage 会把焦点交给新页，先记下焦点是否在 commit 页里。
	commitFocused := t.commitPanel.HasFocus()

	t.activeTab = tag
	t.mainPanels.SwitchToPage(tag)
	t.applyTabStyles()

	// change 页暂无焦点控件，避免焦点留在已隐藏的面板里。
	if tag == tabChange && commitFocused {
		t.app.SetFocus(t.branchList)
	}
}

func (t *TUI) applyTabStyles() {
	t.setTabStyle(t.changeTab, t.activeTab == tabChange)
	t.setTabStyle(t.commitTab, t.activeTab == tabCommit)
}

func (t *TUI) setTabStyle(button *tview.Button, active bool) {
	style := t.tabStyle
	if active {
		style = t.tabActiveStyle
	}
	button.SetStyle(style).SetActivatedStyle(style)
}

// refreshCommit 更新 commit 面板与 commit 信息，并异步请求文件列表。
// force 用于宽度变化等必须重绘 commitInfo 的场景；否则仅在选中 commit 变化时重绘。
func (t *TUI) refreshCommit(force bool) {
	row := t.commitList.currentItem()
	if row >= len(t.Commits) {
		return
	}

	commit := t.Commits[row]
	changed := commit.Hash != t.CurrentCommit.Hash
	t.CurrentCommit = commit

	if force || changed {
		text := fmt.Sprintf("[aqua]%v[-] [gray]%v[-]  [::b]%v[-::-]", tview.Escape(commit.Author), commit.Date.Format("2006-01-02 15:04:05"), tview.Escape(commit.Subject))
		if body := strings.TrimLeft(commit.Body, "\r\n"); body != "" {
			first, _, more := strings.Cut(body, "\n")
			if t.infoWidth > 4 {
				if truncate := runewidth.Truncate(first, t.infoWidth-4, ""); truncate != first {
					first, more = truncate, true
				}
			}
			text += "\n" + tview.Escape(first)
			if more {
				text += " [gray]...[-]"
			}
		}
		t.commitInfo.SetText(text)
	}

	t.refreshFiles(commit)
}

func (t *TUI) refreshFiles(commit core.Commit) {
	t.files.Request(filesRequest{commit: commit})
}

// showFilesLoading 保留旧文件树，只通过 statusBar 提示加载。
func (t *TUI) showFilesLoading(_ filesRequest) {
	// 保留旧 diff 内容，但停掉在途请求，避免旧结果在文件加载期间回写面板。
	t.diff.Cancel()
	t.statusDone("diff", "")

	t.filesLoading = true
	t.statusStart("files", "file changes")
}

func (t *TUI) showFiles(req filesRequest, changes []core.FileChange, err error) {
	t.filesLoading = false

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
		fileRoot = buildFileTree(changes, t.selectedStyle)
	}
	t.fileList.SetTitle(title)
	t.fileList.SetRoot(fileRoot)

	switch {
	case err != nil:
		t.clearFileDiff("failed to load file changes")
		t.files.Cancel() // 下次刷新/重新选中时重试
	case len(changes) == 0:
		t.clearFileDiff("no changes")
	default:
		first := firstFileNode(fileRoot)
		if first == nil {
			t.clearFileDiff("select a file")
			break
		}
		t.fileList.SetCurrentNode(first)
		t.showFileDiffFor(req.commit, first)
	}
	t.statusDone("files", "")
}

func (t *TUI) showFileDiff(node *tview.TreeNode) {
	// 文件列表加载中时，旧树节点属于旧 commit，不能拿来请求新 commit 的 diff。
	if t.filesLoading {
		return
	}
	t.showFileDiffFor(t.CurrentCommit, node)
}

func (t *TUI) showFileDiffFor(commit core.Commit, node *tview.TreeNode) {
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		// 目录节点提示选择文件；错误/无变更占位节点保留当前内容
		if _, isFolder := node.GetReference().(string); isFolder {
			t.clearFileDiff("select a file")
		}
		return
	}
	t.diff.Request(diffRequest{commit: commit, change: change})
}

// showDiffLoading 保留旧 diff，只通过 statusBar 提示加载。
func (t *TUI) showDiffLoading(req diffRequest) {
	t.statusStart("diff", "diff "+req.change.Path)
}

func (t *TUI) showDiff(req diffRequest, text string, err error) {
	message := "diff: " + req.change.Path
	switch {
	case err != nil:
		text = "[red]error[-] " + tview.Escape(err.Error())
		message = "diff failed: " + req.change.Path
	case text == "":
		text = "[gray](no diff)[-]"
		message = "no diff: " + req.change.Path
	}
	t.setFileDiff(req.change, text, message)
}

func (t *TUI) setFileDiff(change core.FileChange, text, message string) {
	t.statusDone("diff", message)
	t.fileDiff.SetTitle(" file diff: " + tview.Escape(change.Path) + " ")
	t.fileDiff.SetText(text)
	t.fileDiff.ScrollToBeginning()
}

// clearFileDiff 清空 diff 并让在途请求失效；Loader 会取消对应的 git 进程。
func (t *TUI) clearFileDiff(message string) {
	t.diff.Cancel()
	t.statusDone("diff", message)
	t.fileDiff.SetTitle(" file diff ")
	if message == "" {
		t.fileDiff.SetText("")
	} else {
		t.fileDiff.SetText("[gray]" + tview.Escape(message) + "[-]")
	}
	t.fileDiff.ScrollToBeginning()
}
