package tui

import (
	"context"
	"fmt"
	"sort"
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

	app *tview.Application

	branchList *tview.TreeView
	changeTab  *tview.Button
	commitTab  *tview.Button
	mainPanels *tview.Pages
	statusBar  *tview.TextView

	commitPanel *tview.Flex
	commitList  *laneView
	commitInfo  *tview.TextView
	fileList    *tview.TreeView
	fileDiff    *tview.TextView

	changePanel *tview.Flex
	changeList  *tview.TreeView
	changeDiff  *tview.TextView

	activeTab  string // 当前显示的主面板 tab
	infoWidth  int
	statusMsg  string            // 最近一次操作结果，loading 结束后保留
	statusBusy map[string]string // statusBar 当前展示的 loading 项，key 为槽位

	tabStyle       tcell.Style
	tabActiveStyle tcell.Style
	selectedStyle  tcell.Style
	headerStyle    tcell.Style

	commits      *async.Loader[[]core.Commit, commitsRequest]
	diff         *async.Loader[string, diffRequest]
	files        *async.Loader[[]core.FileChange, filesRequest]
	filesLoading bool // 文件列表加载中：保留旧树并忽略交互

	localDiff         *async.Loader[string, localDiffRequest]
	localFiles        *async.Loader[[]core.FileChange, localFilesRequest]
	localFilesLoading bool // 本地变更列表加载中：保留旧树并忽略交互
}

type commitsRequest struct{}

type filesRequest struct {
	commit core.Commit
}

type diffRequest struct {
	commit core.Commit
	change core.FileChange
}

type localFilesRequest struct{}

type localDiffRequest struct {
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
			app.SetFocus(t.nextFocus(1))
			return nil
		case event.Key() == tcell.KeyBacktab:
			app.SetFocus(t.nextFocus(-1))
			return nil
		}
		return event
	})

	t.app = app

	t.initAsync()
	defer t.commits.Close()
	defer t.files.Close()
	defer t.diff.Close()
	defer t.localFiles.Close()
	defer t.localDiff.Close()

	t.refreshCommit(true)

	return app.Run()
}

func (t *TUI) initAsync() {
	post := func(f func()) { t.app.QueueUpdateDraw(f) }

	t.commits = async.New(async.Config[[]core.Commit, commitsRequest]{
		Key: func(commitsRequest) string { return "commits" },
		Load: func(ctx context.Context, _ commitsRequest) ([]core.Commit, error) {
			return core.GitCommits(ctx, t.Repo)
		},
		Post:      post,
		OnLoading: t.showCommitsLoading,
		OnResult:  t.showCommits,
	})

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
			return loadDiff(t.Diff(ctx, req.commit, req.change))
		},
		Post:      post,
		OnLoading: t.showFileDiffLoading,
		OnResult:  t.showFileDiff,
	})

	t.localFiles = async.New(async.Config[[]core.FileChange, localFilesRequest]{
		Key: func(localFilesRequest) string { return "local" },
		Load: func(ctx context.Context, _ localFilesRequest) ([]core.FileChange, error) {
			return t.LocalChanges(ctx)
		},
		Post:      post,
		OnLoading: t.showLocalLoading,
		OnResult:  t.showLocal,
	})

	t.localDiff = async.New(async.Config[string, localDiffRequest]{
		Key: func(req localDiffRequest) string {
			return fmt.Sprintf("%d\x00%s", req.change.Scope, req.change.Path)
		},
		Load: func(ctx context.Context, req localDiffRequest) (string, error) {
			return loadDiff(t.LocalDiff(ctx, req.change))
		},
		Post:      post,
		OnLoading: t.showLocalDiffLoading,
		OnResult:  t.showLocalDiff,
	})
}

func (t *TUI) setupUI() tview.Primitive {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	// 背景跟随终端的默认背景色，而不是 ANSI 黑：
	// 终端窗口在字符网格外可能露出自己的底色（例如底部圆角的留白），
	// 用默认色能让 UI 和窗口边缘保持一致。
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault

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
		color := tcell.ColorDarkGray
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

	// scrollFocusable 是内嵌 *tview.Box 的控件（TextView / TreeView 都满足）。
	type scrollFocusable interface {
		tview.Primitive
		InRect(x, y int) bool
		SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse)) *tview.Box
	}

	// focusIfScrolled 在滚轮事件上同步聚焦，边框状态和焦点保持一致。
	focusIfScrolled := func(p tview.Primitive, action tview.MouseAction) {
		switch action {
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !p.HasFocus() {
				t.app.SetFocus(p)
			}
		}
	}

	// focusOnScroll 让滚轮滚动时同步聚焦。
	focusOnScroll := func(p scrollFocusable) {
		p.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
			if !p.InRect(event.Position()) {
				return action, event
			}
			focusIfScrolled(p, action)
			return action, event
		})
	}

	// focusOnScrollTree 同上，并跳过 MouseLeftDown 的默认处理：
	// TreeView 按下鼠标后会重绘，并把视口外的选中项拉回可见范围，
	// 导致随后的点击按新的 offsetY 命中错误的行。焦点只能在这里自己补。
	focusOnScrollTree := func(p scrollFocusable) {
		p.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
			if !p.InRect(event.Position()) {
				return action, event
			}
			if action == tview.MouseLeftDown {
				if !p.HasFocus() {
					t.app.SetFocus(p)
				}
				return tview.MouseMove, event
			}
			focusIfScrolled(p, action)
			return action, event
		})
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

	focusOnScrollTree(fileList)

	fileList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.filesLoading {
			return
		}
		if len(node.GetChildren()) == 0 {
			t.selectFile(node)
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	fileList.SetChangedFunc(func(node *tview.TreeNode) {
		t.selectFile(node)
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

	focusOnScroll(fileDiff)

	commitDetail := tview.NewFlex().
		AddItem(fileList, 40, 1, false).
		AddItem(fileDiff, 0, 1, false)

	commitPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(commitList, 18, 0, true).
		AddItem(commitInfo, 4, 0, false).
		AddItem(commitDetail, 0, 1, false)

	changeList := tview.NewTreeView().
		SetRoot(buildLocalTree(nil, selectedStyle, headerStyle)).
		SetTopLevel(1).
		SetGraphics(false)

	changeList.SetTitle(" local changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(changeList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(changeList.Box, false) })

	focusOnScrollTree(changeList)

	changeList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.localFilesLoading {
			return
		}
		if len(node.GetChildren()) == 0 {
			t.selectLocalFile(node)
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	changeList.SetChangedFunc(func(node *tview.TreeNode) {
		t.selectLocalFile(node)
	})

	changeDiff := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetText("[gray]select a file[-]")

	changeDiff.SetTitle(" file diff ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(changeDiff.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(changeDiff.Box, false) })

	focusOnScroll(changeDiff)

	changePanel := tview.NewFlex().
		AddItem(changeList, 40, 1, true).
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

	var headNode *tview.TreeNode

	for _, b := range t.Branches {
		parent := localNode
		if b.Remote {
			parent = remoteNode
		}

		// HEAD 指向的本地分支前面加绿色对勾；其余本地分支用等宽空格对齐。
		// Escape 防止分支名里的 [ 被当作颜色标签。
		label := tview.Escape(b.Name)
		isHead := !b.Remote && b.Name == t.HeadRef
		switch {
		case isHead:
			label = "[green]✓[-] " + label
		case !b.Remote:
			label = "  " + label
		}

		child := tview.NewTreeNode(label).
			SetSelectedTextStyle(selectedStyle).
			SetSelectedFunc(func() {
				// commits 刷新后行号会变化，每次跳转都按当前列表查询。
				if row := t.commitRow(b.Hash); row >= 0 {
					commitList.setCurrentItem(row)
				}
			})

		if isHead {
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

	focusOnScrollTree(branchList)

	branchHead := tview.NewTextView().
		SetText(t.HeadRef).
		SetWrap(false)

	branchHead.SetTitle(" "+t.RepoName+" ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	branchHead.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick || !branchHead.InRect(event.Position()) {
			return action, event
		}
		if row := t.commitRow(t.HeadHash); row >= 0 {
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

	// SetRoot 只会对 commitList 触发 focus 回调，其余需手动初始化为未聚焦
	setFocusBorder(branchList.Box, false)
	setFocusBorder(commitList.Box, true)
	setFocusBorder(fileList.Box, false)
	setFocusBorder(fileDiff.Box, false)
	setFocusBorder(changeList.Box, false)
	setFocusBorder(changeDiff.Box, false)

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
	t.changeList = changeList
	t.changeDiff = changeDiff
	t.activeTab = tabCommit
	t.statusBar = statusBar
	t.statusBusy = make(map[string]string)
	t.tabStyle = defaultStyle
	t.tabActiveStyle = selectedStyle
	t.selectedStyle = selectedStyle
	t.headerStyle = headerStyle
	t.switchTabStyles()

	return root
}

// nextFocus 在当前 tab 的循环顺序里前进 step（±1）。
func (t *TUI) nextFocus(step int) tview.Primitive {
	chain := t.focusChain()
	for i, p := range chain {
		if p.HasFocus() {
			return chain[(i+step+len(chain))%len(chain)]
		}
	}
	// 焦点不在链上（例如刚点到面板空白处）时的兜底。
	if step < 0 {
		return chain[len(chain)-1]
	}
	return chain[0]
}

// focusChain 是当前 tab 下 Tab/Backtab 的循环顺序。
func (t *TUI) focusChain() []tview.Primitive {
	if t.activeTab == tabChange {
		return []tview.Primitive{t.branchList, t.changeList, t.changeDiff}
	}
	return []tview.Primitive{t.branchList, t.commitList, t.fileList, t.fileDiff}
}

func (t *TUI) switchTab(tag string) {
	if tag == t.activeTab {
		// 再点一次当前 tab 也刷新，方便在外部 git 操作后重新读取数据。
		switch tag {
		case tabChange:
			t.reloadLocal()
		case tabCommit:
			t.reloadCommits()
		}
		return
	}

	// 记下焦点是否在即将隐藏的那一页里；
	// SwitchToPage 之后子控件的 hasFocus 标记不会清掉，事后判断不可靠。
	var hiddenFocused bool
	if tag == tabChange {
		hiddenFocused = t.commitPanel.HasFocus()
	} else {
		hiddenFocused = t.changePanel.HasFocus()
	}

	t.activeTab = tag
	t.mainPanels.SwitchToPage(tag)
	t.switchTabStyles()

	if tag == tabChange {
		t.reloadLocal()
		// 焦点原本在左侧分支列表时保留，避免切 tab 抢走焦点。
		if hiddenFocused {
			t.app.SetFocus(t.changeList)
		}
		return
	}

	t.reloadCommits()
	if hiddenFocused {
		t.app.SetFocus(t.commitList)
	}
}

func (t *TUI) switchTabStyles() {
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

	t.requestFiles(commit)
}

func (t *TUI) selectFile(node *tview.TreeNode) {
	// 文件列表加载中时，旧树节点属于旧 commit，不能拿来请求新 commit 的 diff。
	if t.filesLoading {
		return
	}
	t.requestFileDiff(t.CurrentCommit, node)
}

func (t *TUI) selectLocalFile(node *tview.TreeNode) {
	// 列表加载中时，旧树节点属于旧数据，不能拿来发起新的 diff 请求。
	if t.localFilesLoading {
		return
	}
	t.requestLocalDiff(node)
}

// commitRow 返回 hash 在提交列表中的行号；hash 为空或不存在时返回 -1。
// 刷新 commits 后行号会整体移动，分支跳转必须按当前列表重新查询。
func (t *TUI) commitRow(hash string) int {
	if hash == "" {
		return -1
	}
	for i, commit := range t.Commits {
		if commit.Hash == hash {
			return i
		}
	}
	return -1
}

// localChange 返回当前选中项对应的文件变更；目录/分组节点返回零值。
func (t *TUI) localChange() core.FileChange {
	if t.changeList == nil {
		return core.FileChange{}
	}
	node := t.changeList.GetCurrentNode()
	if node == nil {
		return core.FileChange{}
	}
	change, _ := node.GetReference().(core.FileChange)
	return change
}

/** COMMIT */

// reloadCommits 重新读取 git log；加载期间保留旧列表，避免切 tab 时闪空。
func (t *TUI) reloadCommits() {
	t.commits.Reload(commitsRequest{})
}

func (t *TUI) showCommitsLoading(_ commitsRequest) {
	t.statusStart("commits", "commits")
}

func (t *TUI) showCommits(_ commitsRequest, commits []core.Commit, err error) {
	if err != nil {
		// 保留旧列表，只提示失败原因；下次切 tab 会重试。
		t.statusDone("commits", "commits: "+err.Error())
		return
	}
	t.statusDone("commits", "")

	// 按 hash 恢复选中项：新提交只会插到列表前面，不应该把用户甩到别的提交。
	row := t.commitList.currentItem()
	previous := ""
	if row < len(t.Commits) {
		previous = t.Commits[row].Hash
	}

	t.Commits = commits
	t.commitList.setLines(renderGraph(commits, core.ComputeGraph(commits)))

	if len(commits) == 0 {
		t.CurrentCommit = core.Commit{}
		t.filesLoading = false
		t.files.Cancel()
		t.statusDone("files", "")
		t.commitInfo.SetText("")
		t.fileList.SetTitle(" file changes ")
		t.fileList.SetRoot(tview.NewTreeNode(""))
		t.clearFileDiff("no commits")
		return
	}

	if found := t.commitRow(previous); found >= 0 {
		row = found
	}
	t.commitList.setCurrentItem(row)
	t.refreshCommit(true)
}

/** FILES */

// requestFiles 请求 commit 的文件列表；同一 commit 的重复请求由 Loader 去重。
func (t *TUI) requestFiles(commit core.Commit) {
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
		node := firstFileNode(fileRoot)
		if node == nil {
			t.clearFileDiff("select a file")
			break
		}
		t.fileList.SetCurrentNode(node)
		t.requestFileDiff(req.commit, node)
	}
	t.statusDone("files", "")
}

/** FILE DIFF */

func (t *TUI) requestFileDiff(commit core.Commit, node *tview.TreeNode) {
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

// showFileDiffLoading 保留旧 diff，只通过 statusBar 提示加载。
func (t *TUI) showFileDiffLoading(req diffRequest) {
	t.statusStart("diff", "diff "+req.change.Path)
}

func (t *TUI) showFileDiff(req diffRequest, text string, err error) {
	message := "diff: " + req.change.Path
	switch {
	case err != nil:
		text = "[red]error[-] " + tview.Escape(err.Error())
		message = "diff failed: " + req.change.Path
	case text == "":
		text = "[gray](no diff)[-]"
		message = "no diff: " + req.change.Path
	}
	t.setDiff(t.fileDiff, "diff", req.change, text, message)
}

// clearFileDiff 清空 diff 并让在途请求失效；Loader 会取消对应的 git 进程。
func (t *TUI) clearFileDiff(message string) {
	t.diff.Cancel()
	t.clearDiff(t.fileDiff, "diff", message)
}

/** LOCAL */

// reloadLocal 重新读取本地变更；加载期间保留旧列表，避免切 tab 时闪空。
func (t *TUI) reloadLocal() {
	t.localFiles.Reload(localFilesRequest{})
}

// showLocalLoading 保留旧变更树，只通过 statusBar 提示加载。
func (t *TUI) showLocalLoading(_ localFilesRequest) {
	// 保留旧 diff 内容，但停掉在途请求并丢弃缓存：
	// 文件内容可能已经变化，不能再用 scope+path 命中旧结果。
	t.localDiff.Reset()
	t.statusDone("local-diff", "")

	t.localFilesLoading = true
	t.statusStart("local", "local changes")
}

func (t *TUI) showLocal(_ localFilesRequest, changes []core.FileChange, err error) {
	t.localFilesLoading = false

	// 刷新后尽量恢复原来的选中文件，避免每次切 tab 都跳回第一项。
	previous := t.localChange()

	title := " local changes "
	var changeRoot *tview.TreeNode
	switch {
	case err != nil:
		changeRoot = tview.NewTreeNode("")
		changeRoot.AddChild(tview.NewTreeNode("[red]error[-] " + tview.Escape(err.Error())))
	case len(changes) == 0:
		title = " local changes (0) "
		changeRoot = tview.NewTreeNode("")
		changeRoot.AddChild(tview.NewTreeNode("[gray](no changes)[-]"))
	default:
		title = fmt.Sprintf(" local changes (%d) ", len(changes))
		changeRoot = buildLocalTree(changes, t.selectedStyle, t.headerStyle)
	}
	t.changeList.SetTitle(title)
	t.changeList.SetRoot(changeRoot)

	switch {
	case err != nil:
		t.clearLocalDiff("failed to load local changes")
		t.localFiles.Cancel() // 下次切回时重试
	case len(changes) == 0:
		t.clearLocalDiff("no changes")
	default:
		node := findLocalNode(changeRoot, previous)
		if node == nil {
			node = firstFileNode(changeRoot)
		}
		if node == nil {
			t.clearLocalDiff("select a file")
			break
		}
		t.changeList.SetCurrentNode(node)
		t.requestLocalDiff(node)
	}
	t.statusDone("local", "")
}

/** LOCAL DIFF */

func (t *TUI) requestLocalDiff(node *tview.TreeNode) {
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		// 目录/分组节点提示选择文件；错误/无变更占位节点保留当前内容
		if _, isFolder := node.GetReference().(string); isFolder {
			t.clearLocalDiff("select a file")
		}
		return
	}
	t.localDiff.Request(localDiffRequest{change: change})
}

// showLocalDiffLoading 保留旧 diff，只通过 statusBar 提示加载。
func (t *TUI) showLocalDiffLoading(req localDiffRequest) {
	t.statusStart("local-diff", "diff "+req.change.Path)
}

func (t *TUI) showLocalDiff(req localDiffRequest, text string, err error) {
	message := "diff: " + req.change.Path
	switch {
	case err != nil:
		text = "[red]error[-] " + tview.Escape(err.Error())
		message = "diff failed: " + req.change.Path
	case text == "":
		text = "[gray](no diff)[-]"
		message = "no diff: " + req.change.Path
	}
	t.setDiff(t.changeDiff, "local-diff", req.change, text, message)
}

// clearLocalDiff 清空 diff 并让在途请求失效；Loader 会取消对应的 git 进程。
func (t *TUI) clearLocalDiff(message string) {
	t.localDiff.Cancel()
	t.clearDiff(t.changeDiff, "local-diff", message)
}

/** DIFF */

func (t *TUI) setDiff(view *tview.TextView, slot string, change core.FileChange, text, message string) {
	t.statusDone(slot, message)
	view.SetTitle(" file diff: " + tview.Escape(change.Path) + " ")
	view.SetText(text)
	view.ScrollToBeginning()
}

func (t *TUI) clearDiff(view *tview.TextView, slot, message string) {
	t.statusDone(slot, message)
	view.SetTitle(" file diff ")
	if message == "" {
		view.SetText("")
	} else {
		view.SetText("[gray](" + tview.Escape(message) + ")[-]")
	}
	view.ScrollToBeginning()
}

/** STATUS */

// statusStart 标记一个槽位正在加载；同一槽位重复调用只更新文案。
func (t *TUI) statusStart(slot, message string) {
	if t.statusBar == nil {
		return
	}
	if t.statusBusy == nil {
		t.statusBusy = make(map[string]string)
	}
	t.statusBusy[slot] = message
	t.renderStatus()
}

// statusDone 结束槽位的 loading；message 非空时同时更新最近完成信息。
func (t *TUI) statusDone(slot, message string) {
	if t.statusBar == nil {
		return
	}
	delete(t.statusBusy, slot)
	if message != "" {
		t.statusMsg = message
	}
	t.renderStatus()
}

func (t *TUI) renderStatus() {
	if len(t.statusBusy) == 0 {
		if t.statusMsg == "" {
			t.statusBar.SetText("")
			return
		}
		t.statusBar.SetText("[gray]" + tview.Escape(t.statusMsg) + "[-]")
		return
	}

	messages := make([]string, 0, len(t.statusBusy))
	for _, message := range t.statusBusy {
		messages = append(messages, tview.Escape(message))
	}
	sort.Strings(messages)
	t.statusBar.SetText("[gray]loading " + strings.Join(messages, ", ") + "…[-]")
}
