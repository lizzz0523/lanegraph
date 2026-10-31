package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"lanegraph/internal/async"
	"lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
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

	repoHead   *tview.TextView
	changeTab  *tview.Button
	commitTab  *tview.Button
	filterText *tview.TextArea
	branchList *tview.TreeView
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

	activeTab     string // 当前显示的主面板 tab
	infoWidth     int
	statusMsg     string            // 最近一次操作结果，loading 结束后保留
	statusBusy    map[string]string // statusBar 当前展示的 loading 项，key 为槽位
	pendingBranch string            // checkout 后等待 refs 刷新完成再选中的分支
	quitting      bool              // 退出流程已启动，忽略重复退出

	tabStyle            tcell.Style
	tabActiveStyle      tcell.Style
	selectedStyle       tcell.Style
	headerStyle         tcell.Style
	headerSelectedStyle tcell.Style

	refs    *async.Loader[core.Refs, refsRequest]
	commits *async.Loader[[]core.Commit, commitsRequest]

	diff  *async.Loader[string, diffRequest]
	files *async.Loader[[]core.FileChange, filesRequest]

	localDiff  *async.Loader[string, localDiffRequest]
	localFiles *async.Loader[[]core.FileChange, localFilesRequest]

	checkout *async.Loader[string, checkoutRequest]
}

type refsRequest struct{}

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

type checkoutRequest struct {
	branch core.Branch
}

func New(st *core.State) *TUI {
	return &TUI{State: st}
}

func (t *TUI) Run() error {
	app := tview.NewApplication().
		SetRoot(t.setupUI(), true).
		EnableMouse(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		filtering := t.filterText != nil && t.filterText.HasFocus()
		switch {
		// filter 输入框里要能正常输入 q；Ctrl+Q 仍然随时可退出。
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q' && !filtering):
			t.quit()
			return nil
		case event.Key() == tcell.KeyEscape && filtering:
			t.filterText.SetText("", false)
			app.SetFocus(t.branchList)
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

	t.initLoaders()
	defer t.closeLoaders()

	t.refreshTabStyles()
	t.refreshBranches("")
	t.refreshCommit(true)

	return app.Run()
}

// quit 先停止 Loader，再等后台 goroutine 退出，最后停止事件循环。
// 不能在事件循环里同步等待，否则 Post 无法被消费。
func (t *TUI) quit() {
	if t.quitting {
		return
	}
	t.quitting = true

	// Close 是非阻塞的，先阻止新的请求并取消在途 Load。
	t.closeLoaders()

	go func() {
		t.waitLoaders()
		t.app.Stop()
	}()
}

func (t *TUI) initLoaders() {
	post := func(f func()) { t.app.QueueUpdateDraw(f) }

	t.refs = async.New(async.Config[core.Refs, refsRequest]{
		Key: func(refsRequest) string { return "refs" },
		Load: func(ctx context.Context, _ refsRequest) (core.Refs, error) {
			return t.Refs(ctx)
		},
		Post:      post,
		OnLoading: t.showRefsLoading,
		OnResult:  t.showRefs,
	})

	t.commits = async.New(async.Config[[]core.Commit, commitsRequest]{
		Key: func(commitsRequest) string { return "commits" },
		Load: func(ctx context.Context, _ commitsRequest) ([]core.Commit, error) {
			return t.History(ctx)
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

	t.checkout = async.New(async.Config[string, checkoutRequest]{
		Key: func(req checkoutRequest) string { return req.branch.Name },
		Load: func(ctx context.Context, req checkoutRequest) (string, error) {
			return t.Checkout(ctx, req.branch)
		},
		Post:      post,
		OnLoading: t.showCheckoutLoading,
		OnResult:  t.showCheckout,
	})
}

func (t *TUI) loaders() []async.LoaderLifecycle {
	return []async.LoaderLifecycle{
		t.refs,
		t.commits,
		t.files,
		t.diff,
		t.localFiles,
		t.localDiff,
		t.checkout,
	}
}

func (t *TUI) closeLoaders() {
	for _, loader := range t.loaders() {
		loader.Close()
	}
}

func (t *TUI) waitLoaders() {
	for _, loader := range t.loaders() {
		loader.Wait()
	}
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

	commitList.SetTitle(" all commits ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(commitList.Box, false) })

	commitList.setLines(renderGraph(t.Commits, core.ComputeGraph(t.Commits)))

	commitList.setSelectedFunc(func() {
		t.refreshCommit(false)
	})

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
				// 宽度变化只需重排文本，不修改选择状态。
				commitInfo.SetText(renderCommit(t.CurrentCommit, width))
			}
			return x, y, width, height
		})

	fileList := tview.NewTreeView().
		SetRoot(buildFileTree(nil, selectedStyle)).
		SetTopLevel(1).
		SetGraphics(false)

	fileList.SetTitle(" file changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(fileList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(fileList.Box, false) })

	focusOnScrollTree(fileList)

	// 文件预览由 changed 负责，这里只处理目录展开/收起。
	fileList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.files.Loading() || len(node.GetChildren()) == 0 {
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

	// 文件预览由 changed 负责，这里只处理目录展开/收起。
	changeList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.localFiles.Loading() || len(node.GetChildren()) == 0 {
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

	filterText := tview.NewTextArea()

	filterText.SetTitle(" filter ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(filterText.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(filterText.Box, false) })

	filterText.SetChangedFunc(func() {
		t.refreshBranches(t.branchName())
	})

	branchList := tview.NewTreeView().
		SetTopLevel(1).
		SetGraphics(false)

	branchList.SetTitle(" branches ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(branchList.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(branchList.Box, false) })

	// 空格 checkout 当前分支；TreeView 默认把空格当选中，必须在这里先截获。
	branchList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune && event.Rune() == ' ' {
			t.checkoutBranch()
			return nil
		}
		return event
	})

	focusOnScrollTree(branchList)

	repoHead := tview.NewTextView().
		SetText(t.HeadRef).
		SetWrap(false)

	repoHead.SetTitle(" "+t.RepoName+" ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	repoHead.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick || !repoHead.InRect(event.Position()) {
			return action, event
		}
		if row := t.commitRow(t.HeadHash); row >= 0 {
			commitList.setCurrentItem(row)
		}
		if node := t.branchNode(t.HeadRef); node != nil {
			branchList.SetCurrentNode(node)
		}
		t.app.SetFocus(branchList)
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
		AddItem(repoHead, 3, 0, false).
		AddItem(mainTabs, 2, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(filterText, 3, 0, false).
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

	t.repoHead = repoHead
	t.changeTab = changeTab
	t.commitTab = commitTab
	t.filterText = filterText
	t.branchList = branchList
	t.mainPanels = mainPanels
	t.statusBar = statusBar

	t.commitPanel = commitPanel
	t.commitList = commitList
	t.commitInfo = commitInfo
	t.fileList = fileList
	t.fileDiff = fileDiff

	t.changePanel = changePanel
	t.changeList = changeList
	t.changeDiff = changeDiff

	t.activeTab = tabCommit
	t.statusBusy = make(map[string]string)

	t.tabStyle = defaultStyle
	t.tabActiveStyle = selectedStyle
	t.selectedStyle = selectedStyle
	t.headerStyle = headerStyle
	t.headerSelectedStyle = headerSelectedStyle

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
		return []tview.Primitive{t.filterText, t.branchList, t.changeList, t.changeDiff}
	}
	return []tview.Primitive{t.filterText, t.branchList, t.commitList, t.fileList, t.fileDiff}
}

func (t *TUI) switchTab(tag string) {
	if tag == t.activeTab {
		// 再点一次当前 tab 也刷新，方便在外部 git 操作后重新读取数据。
		t.refreshTab(tag)
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
	t.refreshTabStyles()
	t.refreshTab(tag)

	// 焦点原本在内容区时，切页后交给新页的列表，避免焦点留在被隐藏的页面上；
	// 焦点在左侧分支列表时 hiddenFocused 为 false，保持不动。
	if hiddenFocused {
		if tag == tabChange {
			t.app.SetFocus(t.changeList)
		} else {
			t.app.SetFocus(t.commitList)
		}
	}
}

// refreshTab 刷新仓库级 refs 和当前 tab 的数据。
func (t *TUI) refreshTab(tag string) {
	t.reloadRefs()
	if tag == tabChange {
		t.reloadLocal()
		return
	}
	t.reloadCommits()
}

func (t *TUI) refreshTabStyles() {
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

// refreshBranches 按当前 Branches 重建分支树；selected 为要选中的分支名，
// 为空或找不到时默认选中第一个分支。
func (t *TUI) refreshBranches(selected string) {
	if t.branchList == nil {
		return
	}

	// 隐藏 root，用 Local / Remote 作为分组标题。
	localNode := tview.NewTreeNode("Local").
		SetTextStyle(t.headerStyle).
		SetSelectedTextStyle(t.headerSelectedStyle)
	remoteNode := tview.NewTreeNode("Remote").
		SetTextStyle(t.headerStyle).
		SetSelectedTextStyle(t.headerSelectedStyle)
	branchRoot := tview.NewTreeNode("").
		AddChild(localNode).
		AddChild(remoteNode)

	filter := ""
	if t.filterText != nil {
		filter = strings.ToLower(strings.TrimSpace(t.filterText.GetText()))
	}

	for _, b := range t.Branches {
		// 本地和远端分支都按完整分支名做大小写不敏感的 contains 过滤；
		// 远端分支的完整名带 remote 前缀（如 origin/foo）。
		if filter != "" && !strings.Contains(strings.ToLower(b.Name), filter) {
			continue
		}

		parent := localNode
		if b.Remote {
			parent = remoteNode
		}

		// HEAD 指向的本地分支前面加绿色对勾；其余本地分支用分支符号占位对齐。
		// Escape 防止分支名里的 [ 被当作颜色标签。
		label := tview.Escape(b.Name)
		isHead := !b.Remote && b.Name == t.HeadRef
		switch {
		case isHead:
			label = "[green]✓[-] " + label
		case !b.Remote:
			label = "[gray]↘[-] " + label
		}

		child := tview.NewTreeNode(label).
			SetReference(b).
			SetSelectedTextStyle(t.selectedStyle).
			SetSelectedFunc(func() {
				// commits 刷新后行号会变化，每次跳转都按当前列表查询。
				if row := t.commitRow(b.Hash); row >= 0 {
					t.commitList.setCurrentItem(row)
				}
			})
		parent.AddChild(child)
	}

	if len(localNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(localNode)
	}
	if len(remoteNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(remoteNode)
	}

	t.branchList.SetRoot(branchRoot)

	if node := t.branchNode(selected); node != nil {
		t.branchList.SetCurrentNode(node)
		return
	}
	// 默认选中第一个分支，而不是分组标题
	if children := localNode.GetChildren(); len(children) > 0 {
		t.branchList.SetCurrentNode(children[0])
	} else if children := remoteNode.GetChildren(); len(children) > 0 {
		t.branchList.SetCurrentNode(children[0])
	}
}

// refreshCommit 刷新选中 commit 的信息，并异步请求文件列表。
// force 为 true 时无论选中项是否变化都重绘 commitInfo。
func (t *TUI) refreshCommit(force bool) {
	row := t.commitList.currentItem()
	if row >= len(t.Commits) {
		return
	}

	commit := t.Commits[row]
	changed := commit.Hash != t.CurrentCommit.Hash
	t.CurrentCommit = commit

	if force || changed {
		t.commitInfo.SetText(renderCommit(commit, t.infoWidth))
	}
	t.requestFiles(commit)
}

func (t *TUI) selectFile(node *tview.TreeNode) {
	// 文件列表加载中时，旧树节点属于旧 commit，不能拿来请求新 commit 的 diff。
	if t.files.Loading() {
		return
	}
	t.requestFileDiff(t.CurrentCommit, node)
}

func (t *TUI) selectLocalFile(node *tview.TreeNode) {
	// 列表加载中时，旧树节点属于旧数据，不能拿来发起新的 diff 请求。
	if t.localFiles.Loading() {
		return
	}
	t.requestLocalDiff(node)
}

// checkoutBranch 对当前选中的分支执行 checkout：
// 本地分支直接切换；远端分支切换到同名本地分支，不存在时创建跟踪分支。
func (t *TUI) checkoutBranch() {
	if t.checkout == nil || t.checkout.Loading() {
		return
	}
	node := t.branchList.GetCurrentNode()
	if node == nil {
		return
	}
	t.reloadCheckout(node)
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

// branchNode 返回分支 name 对应的节点，本地和远端都会匹配；name 为空或不存在时返回 nil。
func (t *TUI) branchNode(name string) *tview.TreeNode {
	if name == "" || t.branchList == nil || t.branchList.GetRoot() == nil {
		return nil
	}
	var found *tview.TreeNode
	t.branchList.GetRoot().Walk(func(node, _ *tview.TreeNode) bool {
		branch, ok := node.GetReference().(core.Branch)
		if ok && branch.Name == name {
			found = node
			return false
		}
		return true
	})
	return found
}

// branchName 返回分支列表当前选中的分支名；分组标题返回空。
func (t *TUI) branchName() string {
	if t.branchList == nil {
		return ""
	}
	node := t.branchList.GetCurrentNode()
	if node == nil {
		return ""
	}
	branch, _ := node.GetReference().(core.Branch)
	return branch.Name
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

/** REFS */

// reloadRefs 重新读取仓库级 refs（分支和 HEAD）。
func (t *TUI) reloadRefs() {
	t.refs.Reload(refsRequest{})
}

func (t *TUI) showRefsLoading(_ refsRequest) {
	t.statusStart("branches", "branches")
}

func (t *TUI) showRefs(_ refsRequest, refs core.Refs, err error) {
	selected := t.pendingBranch
	t.pendingBranch = ""
	if selected == "" {
		selected = t.branchName()
	}

	if err != nil {
		t.statusDone("branches", "branches: "+err.Error())
		return
	}
	t.statusDone("branches", "")

	t.ApplyRefs(refs)
	if t.repoHead != nil {
		t.repoHead.SetText(t.HeadRef)
	}
	t.refreshBranches(selected)
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

	// 按 hash 恢复选中项：topo-order 下刷新可能重排已有提交，不能依赖行号不变。
	row := t.commitList.currentItem()
	previous := ""
	if row < len(t.Commits) {
		previous = t.Commits[row].Hash
	}

	t.Commits = commits
	t.commitList.setLines(renderGraph(commits, core.ComputeGraph(commits)))

	if len(commits) == 0 {
		t.CurrentCommit = core.Commit{}
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
	// setCurrentItem 会像用户选中一样触发 refreshCommit，这里不再额外请求。
	t.commitList.setCurrentItem(row)
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

	t.statusStart("files", "file changes")
}

func (t *TUI) showFiles(_ filesRequest, changes []core.FileChange, err error) {
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
		// 失败由 Loader 记录，下次请求同一 commit 时会重试。
		t.clearFileDiff("failed to load file changes")
	case len(changes) == 0:
		t.clearFileDiff("no changes")
	default:
		node := firstFileNode(fileRoot)
		if node == nil {
			t.clearFileDiff("select a file")
			break
		}
		// 只设置选中项；下一次 Draw 的 changed 回调会统一发起 diff 请求。
		t.fileList.SetCurrentNode(node)
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

	t.statusStart("local", "local changes")
}

func (t *TUI) showLocal(_ localFilesRequest, changes []core.FileChange, err error) {
	// 刷新后尽量恢复原来的选中文件，避免每次切 tab 都跳回第一项。
	previous := t.localChange()

	title := " local changes "
	var changeRoot *tview.TreeNode
	switch {
	case err != nil:
		changeRoot = tview.NewTreeNode("")
		changeRoot.AddChild(tview.NewTreeNode("[red]error[-] " + tview.Escape(err.Error())))
	default:
		// 即使没有变更也保留分组 header，和初始化时的占位树保持一致，
		// 避免第一次切到本页时列表从两组 header 跳成一行占位文本。
		title = fmt.Sprintf(" local changes (%d) ", len(changes))
		changeRoot = buildLocalTree(changes, t.selectedStyle, t.headerStyle)
	}
	t.changeList.SetTitle(title)
	t.changeList.SetRoot(changeRoot)

	switch {
	case err != nil:
		// 失败由 Loader 记录，下次请求本地变更时会重试。
		t.clearLocalDiff("failed to load local changes")
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
		// 只设置选中项；下一次 Draw 的 changed 回调会统一发起 diff 请求。
		t.changeList.SetCurrentNode(node)
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

/** CHECKOUT */

// reloadCheckout 通过节点引用取分支并绕过缓存，保证每次按空格都真正执行 git checkout。
func (t *TUI) reloadCheckout(node *tview.TreeNode) {
	branch, ok := node.GetReference().(core.Branch)
	if !ok {
		return // Local / Remote 分组标题
	}
	t.checkout.Reload(checkoutRequest{branch: branch})
}

func (t *TUI) showCheckoutLoading(req checkoutRequest) {
	t.statusStart("checkout", "checkout "+req.branch.Name)
}

func (t *TUI) showCheckout(_ checkoutRequest, branch string, err error) {
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message := strings.Join(strings.Fields(err.Error()), " ")
		t.statusDone("checkout", "checkout failed: "+message)
		return
	}
	t.statusDone("checkout", "checkout "+branch)

	// refs 刷新后选中刚检出的分支；commit 列表里的 HEAD 标记也随 checkout 变化。
	t.pendingBranch = branch
	t.reloadRefs()
	t.reloadCommits()
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
