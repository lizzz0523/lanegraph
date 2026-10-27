package tui

import (
	"context"
	"fmt"
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

	// root 包住整个界面，弹窗作为独立页面叠在 main 上，从而覆盖全屏。
	root       *tview.Pages
	repoHead   *tview.TextView
	changeTab  *tview.Button
	commitTab  *tview.Button
	filterText *tview.TextArea
	branchList *tview.TreeView
	mainPanels *tview.Pages
	statusBar  *tview.TextView
	statusRow  *tview.Flex
	// rebaseContinue / rebaseAbort 只在 rebase 未完成时出现在 statusBar 右侧。
	rebaseContinue *tview.Button
	rebaseAbort    *tview.Button

	commitPanel *tview.Flex
	commitList  *listView
	commitInfo  *tview.TextView
	fileList    *tview.TreeView
	fileDiff    *tview.TextView

	changePanel       *tview.Flex
	changeList        *tview.TreeView
	changeDiff        *tview.TextView
	commitSubject     *tview.TextArea
	commitDescription *tview.TextArea

	historyList *listView
	historyDiff *tview.TextView

	activeTab      string // 当前显示的主面板 tab
	infoWidth      int
	diffWidth      int               // 当前 diff 渲染使用的面板内宽（side-by-side 时有效）
	historyWidth   int               // history 弹窗内 diff 的渲染宽度
	statusMsg      string            // 最近一次操作结果，loading 结束后保留
	statusBusy     map[string]string // statusBar 当前展示的 loading 项，key 为槽位
	pendingBranch  string            // checkout 后等待 refs 刷新完成再选中的分支
	pendingCommit  string            // history 跳转后等待 commits 刷新完成再选中的提交
	promptVisible  bool              // 确认弹窗是否正在显示
	historyVisible bool              // 文件历史弹窗是否正在显示
	remoteExpanded map[string]bool   // 用户手动展开过的 remote 目录，按 remote 名记录
	syncFrame      int               // 分支同步动画当前帧；-1 表示没有在执行同步操作
	syncStop       chan struct{}     // 关闭后停止分支同步动画
	quitting       bool              // 退出流程已启动，忽略重复退出

	tabStyle            tcell.Style
	tabActiveStyle      tcell.Style
	selectedStyle       tcell.Style
	headerStyle         tcell.Style
	headerSelectedStyle tcell.Style
	borderColor         tcell.Color
	borderFocusColor    tcell.Color

	refs    *async.Loader[core.Refs, refsRequest]
	commits *async.Loader[[]core.Commit, commitsRequest]

	diff  *async.Loader[string, diffRequest]
	files *async.Loader[[]core.FileChange, filesRequest]

	localDiff  *async.Loader[string, localDiffRequest]
	localFiles *async.Loader[[]core.FileChange, localFilesRequest]

	fileHistory     *async.Loader[[]core.Commit, historyRequest]
	fileHistoryDiff *async.Loader[string, historyDiffRequest]

	changeOp *async.Loader[string, changeOpRequest]
	checkout *async.Loader[string, checkoutRequest]
	syncOp   *async.Loader[string, syncOpRequest]
	commitOp *async.Loader[string, commitOpRequest]
}

type refsRequest struct{}

type commitsRequest struct{}

type filesRequest struct {
	commit  core.Commit
	compare *core.Commit
}

type diffRequest struct {
	commit  core.Commit
	change  core.FileChange
	compare *core.Commit
	options core.DiffOptions
}

type localFilesRequest struct{}

type localDiffRequest struct {
	change  core.FileChange
	options core.DiffOptions
}

type historyRequest struct {
	path string
}

type historyDiffRequest struct {
	commit  core.Commit
	change  core.FileChange
	options core.DiffOptions
}

// changeOp 是本地变更列表上的操作类型。
type changeOp uint8

const (
	opStage changeOp = iota
	opUnstage
	opDiscard
)

type changeOpRequest struct {
	op     changeOp
	change core.FileChange
}

type checkoutRequest struct {
	branch core.Branch
}

// syncOp 是当前分支的远程同步操作类型。
type syncOp uint8

const (
	opPull syncOp = iota
	opPush
	opFetch
)

type syncOpRequest struct {
	op syncOp
}

// commitOp 是 commit 列表上的操作类型。
type commitOp uint8

const (
	opReset commitOp = iota
	opRebase
	opRebaseContinue
	opRebaseAbort
)

type commitOpRequest struct {
	op        commitOp
	commit    core.Commit
	resetMode core.ResetMode // 仅 reset 使用
}

func New(st *core.State) *TUI {
	return &TUI{State: st}
}

/** RUN */

func (t *TUI) Run() error {
	app := tview.NewApplication().
		SetRoot(t.setupUI(), true).
		EnableMouse(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// history 弹窗显示时，Esc/q 只关闭弹窗；Ctrl+Q 仍然随时可退出。
		if t.historyVisible && (event.Key() == tcell.KeyEscape || (event.Key() == tcell.KeyRune && event.Rune() == 'q')) {
			t.closeHistory()
			return nil
		}

		filtering := t.filterText != nil && t.filterText.HasFocus()
		switch {
		// filter 输入框里要能正常输入 q；Ctrl+Q 仍然随时可退出。
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q' && !filtering && !t.promptVisible):
			t.quit()
			return nil
		case event.Key() == tcell.KeyEscape && filtering:
			t.filterText.SetText("", false)
			app.SetFocus(t.branchList)
			return nil
		case event.Key() == tcell.KeyTab && !t.promptVisible:
			app.SetFocus(t.nextFocus(1))
			return nil
		case event.Key() == tcell.KeyBacktab && !t.promptVisible:
			app.SetFocus(t.nextFocus(-1))
			return nil
		// p / P / f 全局触发当前分支的 pull / push / fetch；filter 输入时不拦截。
		case event.Key() == tcell.KeyRune && !filtering && !t.promptVisible:
			switch event.Rune() {
			case 'p':
				t.syncBranch(opPull)
				return nil
			case 'P':
				t.syncBranch(opPush)
				return nil
			case 'f':
				t.syncBranch(opFetch)
				return nil
			}
		}
		return event
	})

	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		t.drawDiffBadges(screen)
	})

	t.app = app

	t.setupLoaders()
	defer t.closeLoaders()

	t.refreshTabStyles()
	t.refreshBranches("")
	t.refreshCommit(true)

	return app.Run()
}

/** SETUP */

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

	// 对比基准行用深青色背景，和当前选中行的灰色区分。
	highlightStyle := tcell.StyleDefault.
		Foreground(tview.Styles.PrimaryTextColor).
		Background(tcell.NewHexColor(0x2f4f4f))

	// 强调色用和边框高亮一致的 aqua 背景，配黑字保证对比度；
	// 激活 tab 和 statusBar 上的主操作按钮共用。
	accentStyle := tcell.StyleDefault.
		Foreground(tcell.ColorBlack).
		Background(tcell.ColorAqua)

	// 危险操作用红字，配终端默认背景。
	dangerStyle := tcell.StyleDefault.
		Foreground(tcell.ColorIndianRed).
		Background(tview.Styles.PrimitiveBackgroundColor)

	headerStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	headerSelectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	commitList := newListView(selectedStyle, highlightStyle)

	commitList.SetTitle(" all commits ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitList.Box, false) })

	// r 把当前分支 rebase 到选中的 commit；R 把当前分支 reset 到选中的 commit。
	commitList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case 'r':
				t.rebaseCommit()
				return nil
			case 'R':
				t.resetCommit()
				return nil
			}
		}
		return event
	})

	commitList.setLines(renderGraph(t.Commits, core.ComputeGraph(t.Commits)))

	commitList.setSelectedFunc(func() {
		t.refreshCommit(false)
	})
	commitList.setHighlightFunc(func(index int) {
		t.compareCommit(index)
	})

	commitInfo := tview.NewTextView().
		SetWrap(false).
		SetDynamicColors(true)

	commitInfo.SetTitle(" commit ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
			t.syncCommitInfoWidth(commitInfo)
			return commitInfo.GetInnerRect()
		})

	fileList := tview.NewTreeView().
		SetRoot(buildFileTree(nil, selectedStyle)).
		SetTopLevel(1).
		SetGraphics(false)

	fileList.SetTitle(" file changes ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(fileList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(fileList.Box, false) })

	// o 用系统默认应用打开选中的文件，h 查看文件历史。
	fileList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'o':
			t.openFile(fileList.GetCurrentNode())
			return nil
		case 'h':
			t.openHistory(fileList.GetCurrentNode())
			return nil
		}
		return event
	})

	t.focusOnScrollTree(fileList)

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
		SetFocusFunc(func() { t.setFocusBorder(fileDiff.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(fileDiff.Box, false) })

	fileDiff.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'w':
			t.toggleWhitespace()
			return nil
		case 's':
			t.toggleSideBySide()
			return nil
		}
		return event
	})

	// 在控件绘制前按当前内宽重渲染 side-by-side；此时 Flex 已设置本帧的 rect。
	fileDiff.SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
		t.syncDiffViewWidth(fileDiff)
		return fileDiff.GetInnerRect()
	})

	t.focusOnScroll(fileDiff)

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
		SetFocusFunc(func() { t.setFocusBorder(changeList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(changeList.Box, false) })

	// 空格切换暂存状态，d 丢弃改动，o 打开文件，h 查看文件历史；目录节点保留 TreeView 的展开/收起行为。
	changeList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case ' ':
			t.toggleLocalChange()
			return nil
		case 'd':
			t.discardLocalChange()
			return nil
		case 'o':
			t.openFile(changeList.GetCurrentNode())
			return nil
		case 'h':
			t.openHistory(changeList.GetCurrentNode())
			return nil
		}
		return event
	})

	t.focusOnScrollTree(changeList)

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
		SetFocusFunc(func() { t.setFocusBorder(changeDiff.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(changeDiff.Box, false) })

	changeDiff.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'w':
			t.toggleWhitespace()
			return nil
		case 's':
			t.toggleSideBySide()
			return nil
		}
		return event
	})

	// 在控件绘制前按当前内宽重渲染 side-by-side；此时 Flex 已设置本帧的 rect。
	changeDiff.SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
		t.syncDiffViewWidth(changeDiff)
		return changeDiff.GetInnerRect()
	})

	t.focusOnScroll(changeDiff)

	commitSubject := tview.NewTextArea()

	commitSubject.SetTitle(" commit subject ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitSubject.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitSubject.Box, false) })

	commitDescription := tview.NewTextArea()

	commitDescription.SetTitle(" description ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitDescription.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitDescription.Box, false) })

	commitButton := tview.NewButton("Commit").
		SetStyle(selectedStyle).
		SetActivatedStyle(selectedStyle)

	amendCheck := tview.NewCheckbox().
		SetUncheckedStyle(selectedStyle).
		SetCheckedStyle(selectedStyle).
		SetActivatedStyle(selectedStyle)

	amendLabel := tview.NewTextView().
		SetText("Amend")

	amendLabel.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if !amendLabel.InRect(event.Position()) {
			return action, event
		}
		if action == tview.MouseLeftDown {
			amendCheck.SetChecked(!amendCheck.IsChecked())
			return tview.MouseMove, event
		}
		return action, event
	})

	commitAction := tview.NewFlex().
		AddItem(nil, 1, 0, false).
		AddItem(amendCheck, 1, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(amendLabel, 5, 0, false).
		AddItem(nil, 0, 1, false).
		AddItem(commitButton, 16, 0, false).
		AddItem(nil, 1, 0, false)

	changeInfo := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(changeDiff, 0, 1, false).
		AddItem(commitSubject, 3, 0, false).
		AddItem(commitDescription, 5, 0, false).
		AddItem(commitAction, 1, 0, false).
		AddItem(nil, 1, 0, false)

	changePanel := tview.NewFlex().
		AddItem(changeList, 40, 1, true).
		AddItem(changeInfo, 0, 1, false)

	filterText := tview.NewTextArea()

	filterText.SetTitle(" filter ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(filterText.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(filterText.Box, false) })

	filterText.SetChangedFunc(func() {
		t.refreshBranches(t.branchName())
	})

	branchList := tview.NewTreeView().
		SetTopLevel(1).
		SetGraphics(false)

	branchList.SetTitle(" branches ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(branchList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(branchList.Box, false) })

	// 空格 checkout 当前分支；remote 目录则切换展开状态。
	// TreeView 默认把空格当选中，必须在这里先截获。
	branchList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune && event.Rune() == ' ' {
			node := branchList.GetCurrentNode()
			if node != nil {
				if _, isFolder := node.GetReference().(string); isFolder {
					t.toggleRemoteFolder(node)
				} else {
					t.checkoutBranch()
				}
			}
			return nil
		}
		return event
	})

	// 回车或鼠标点击 remote 目录时切换展开状态，点击分支时跳转到对应 commit。
	branchList.SetSelectedFunc(func(node *tview.TreeNode) {
		if _, isFolder := node.GetReference().(string); isFolder {
			t.toggleRemoteFolder(node)
			return
		}
		if branch, ok := node.GetReference().(core.Branch); ok {
			// commits 刷新后行号会变化，每次跳转都按当前列表查询。
			if row := t.commitRow(branch.Hash); row >= 0 {
				t.commitList.setCurrentItem(row)
			}
		}
	})

	t.focusOnScrollTree(branchList)

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

	// Pages 会先把整块区域刷成背景色再画当前页，
	// 所以切换时不会残留上一个面板的像素。
	mainPanels := tview.NewPages().
		AddPage(tabChange, changePanel, true, false).
		AddPage(tabCommit, commitPanel, true, true)

	sidebar := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(repoHead, 3, 0, false).
		AddItem(mainTabs, 2, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(filterText, 3, 0, false).
		AddItem(branchList, 0, 1, false)

	content := tview.NewFlex().
		AddItem(sidebar, 30, 0, false).
		AddItem(mainPanels, 0, 1, true)

	statusBar := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false)

	statusBar.SetBorderPadding(0, 0, 1, 1)

	// 左键按下也要吞掉，否则 Button 会自己 setFocus，抢走面板焦点。
	bindStatusButton := func(button *tview.Button) {
		button.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
			if !button.InRect(event.Position()) {
				return action, event
			}
			if action == tview.MouseLeftDown {
				return tview.MouseConsumed, nil
			}
			return action, event
		})
	}

	// rebase 未完成时，在 statusBar 右侧显示 Continue / Abort；
	// 平时宽度为 0，不占用状态栏空间。
	rebaseContinue := tview.NewButton("Continue rebase")
	rebaseContinue.SetStyle(accentStyle).SetActivatedStyle(accentStyle)
	rebaseContinue.SetSelectedFunc(t.continueRebase)
	bindStatusButton(rebaseContinue)

	rebaseAbort := tview.NewButton("Abort rebase")
	rebaseAbort.SetStyle(dangerStyle).SetActivatedStyle(dangerStyle)
	rebaseAbort.SetSelectedFunc(t.abortRebase)
	bindStatusButton(rebaseAbort)

	statusRow := tview.NewFlex().
		AddItem(statusBar, 0, 1, false).
		AddItem(rebaseContinue, 0, 0, false).
		AddItem(rebaseAbort, 0, 0, false).
		AddItem(nil, 1, 0, false)

	mainPage := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(content, 0, 1, true).
		AddItem(statusRow, 1, 0, false)

	root := tview.NewPages().
		AddPage("main", mainPage, true, true)

	t.root = root
	t.repoHead = repoHead
	t.changeTab = changeTab
	t.commitTab = commitTab
	t.filterText = filterText
	t.branchList = branchList
	t.mainPanels = mainPanels
	t.statusBar = statusBar
	t.statusRow = statusRow
	t.rebaseContinue = rebaseContinue
	t.rebaseAbort = rebaseAbort

	t.commitPanel = commitPanel
	t.commitList = commitList
	t.commitInfo = commitInfo
	t.fileList = fileList
	t.fileDiff = fileDiff

	t.changePanel = changePanel
	t.changeList = changeList
	t.changeDiff = changeDiff
	t.commitSubject = commitSubject
	t.commitDescription = commitDescription

	t.activeTab = tabCommit
	t.statusBusy = make(map[string]string)
	t.syncFrame = -1

	t.tabStyle = defaultStyle
	t.tabActiveStyle = accentStyle
	t.selectedStyle = selectedStyle
	t.headerStyle = headerStyle
	t.headerSelectedStyle = headerSelectedStyle
	// 边框和标题在未聚焦/聚焦时分别使用这两种颜色。
	t.borderColor = tcell.ColorDarkGray
	t.borderFocusColor = tcell.ColorAqua

	// SetRoot 只会对 commitList 触发 focus 回调，其余需手动初始化为未聚焦
	t.setFocusBorder(filterText.Box, false)
	t.setFocusBorder(branchList.Box, false)
	t.setFocusBorder(commitList.Box, true)
	t.setFocusBorder(fileList.Box, false)
	t.setFocusBorder(fileDiff.Box, false)
	t.setFocusBorder(changeList.Box, false)
	t.setFocusBorder(changeDiff.Box, false)
	t.setFocusBorder(commitSubject.Box, false)
	t.setFocusBorder(commitDescription.Box, false)

	return root
}

func (t *TUI) setupLoaders() {
	post := func(f func()) { t.app.QueueUpdateDraw(f) }

	// compareKey 返回 compare 在 Loader 缓存 key 中的标识。
	compareKey := func(compare *core.Commit) string {
		if compare == nil {
			return ""
		}
		return compare.Hash
	}

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
		Key: func(req filesRequest) string {
			return fmt.Sprintf("%s\x00%s", req.commit.Hash, compareKey(req.compare))
		},
		Load: func(ctx context.Context, req filesRequest) ([]core.FileChange, error) {
			return t.Files(ctx, req.commit, req.compare)
		},
		Post:      post,
		OnLoading: t.showFilesLoading,
		OnResult:  t.showFiles,
	})

	t.diff = async.New(async.Config[string, diffRequest]{
		Key: func(req diffRequest) string {
			return fmt.Sprintf("%s\x00%s\x00%s\x00%s", req.commit.Hash, compareKey(req.compare), req.change.Path, req.options)
		},
		Load: func(ctx context.Context, req diffRequest) (string, error) {
			return renderDiff(t.Diff(ctx, req.commit, req.compare, req.change, req.options))
		},
		Post:      post,
		OnLoading: t.showFileDiffLoading,
		OnResult:  t.showFileDiff,
	})

	t.fileHistory = async.New(async.Config[[]core.Commit, historyRequest]{
		Key: func(req historyRequest) string { return req.path },
		Load: func(ctx context.Context, req historyRequest) ([]core.Commit, error) {
			return t.FileHistory(ctx, req.path)
		},
		Post:     post,
		OnResult: t.showHistory,
	})

	t.fileHistoryDiff = async.New(async.Config[string, historyDiffRequest]{
		Key: func(req historyDiffRequest) string {
			return fmt.Sprintf("%s\x00%s\x00%s", req.commit.Hash, req.change.Path, req.options)
		},
		Load: func(ctx context.Context, req historyDiffRequest) (string, error) {
			return renderDiff(t.Diff(ctx, req.commit, nil, req.change, req.options))
		},
		Post:     post,
		OnResult: t.showHistoryDiff,
	})

	t.localFiles = async.New(async.Config[[]core.FileChange, localFilesRequest]{
		Key: func(localFilesRequest) string { return "local" },
		Load: func(ctx context.Context, _ localFilesRequest) ([]core.FileChange, error) {
			return t.LocalFiles(ctx)
		},
		Post:      post,
		OnLoading: t.showLocalLoading,
		OnResult:  t.showLocal,
	})

	t.localDiff = async.New(async.Config[string, localDiffRequest]{
		Key: func(req localDiffRequest) string {
			return fmt.Sprintf("%d\x00%s\x00%s", req.change.Scope, req.change.Path, req.options)
		},
		Load: func(ctx context.Context, req localDiffRequest) (string, error) {
			return renderDiff(t.LocalDiff(ctx, req.change, req.options))
		},
		Post:      post,
		OnLoading: t.showLocalDiffLoading,
		OnResult:  t.showLocalDiff,
	})

	t.changeOp = async.New(async.Config[string, changeOpRequest]{
		Key: func(req changeOpRequest) string {
			return fmt.Sprintf("%d\x00%d\x00%s", req.op, req.change.Scope, req.change.Path)
		},
		Load: func(ctx context.Context, req changeOpRequest) (string, error) {
			switch req.op {
			case opStage:
				return req.change.Path, t.Stage(ctx, req.change)
			case opUnstage:
				return req.change.Path, t.Unstage(ctx, req.change)
			default:
				return req.change.Path, t.Discard(ctx, req.change)
			}
		},
		Post:      post,
		OnLoading: t.showChangeOpLoading,
		OnResult:  t.showChangeOp,
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

	t.syncOp = async.New(async.Config[string, syncOpRequest]{
		Key: func(req syncOpRequest) string { return fmt.Sprintf("%d", req.op) },
		Load: func(ctx context.Context, req syncOpRequest) (string, error) {
			switch req.op {
			case opPull:
				return t.Pull(ctx)
			case opPush:
				return t.Push(ctx)
			default:
				return t.Fetch(ctx)
			}
		},
		Post:      post,
		OnLoading: t.showSyncLoading,
		OnResult:  t.showSync,
	})

	t.commitOp = async.New(async.Config[string, commitOpRequest]{
		Key: func(req commitOpRequest) string {
			return fmt.Sprintf("%d\x00%s\x00%d", req.op, req.commit.Hash, req.resetMode)
		},
		Load: func(ctx context.Context, req commitOpRequest) (string, error) {
			switch req.op {
			case opReset:
				return t.Reset(ctx, req.commit, req.resetMode)
			case opRebaseContinue:
				return t.RebaseContinue(ctx)
			case opRebaseAbort:
				return t.RebaseAbort(ctx)
			default:
				return t.Rebase(ctx, req.commit)
			}
		},
		Post:      post,
		OnLoading: t.showCommitOpLoading,
		OnResult:  t.showCommitOp,
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
		t.fileHistory,
		t.fileHistoryDiff,
		t.changeOp,
		t.checkout,
		t.syncOp,
		t.commitOp,
	}
}

func (t *TUI) waitLoaders() {
	for _, loader := range t.loaders() {
		loader.Wait()
	}
}

func (t *TUI) closeLoaders() {
	for _, loader := range t.loaders() {
		loader.Close()
	}
}

// scrollFocusable 是内嵌 *tview.Box 的控件（TextView / TreeView / List 都满足）。
type scrollFocusable interface {
	tview.Primitive
	InRect(x, y int) bool
	SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse)) *tview.Box
}

// focusOnScroll 让滚轮滚动时同步聚焦。
func (t *TUI) focusOnScroll(p scrollFocusable) {
	p.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if !p.InRect(event.Position()) {
			return action, event
		}
		switch action {
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !p.HasFocus() {
				t.app.SetFocus(p)
			}
		}
		return action, event
	})
}

// focusOnScrollTree 同上，并跳过 MouseLeftDown 的默认处理：
// TreeView 按下鼠标后会重绘，并把视口外的选中项拉回可见范围，
// 导致随后的点击按新的 offsetY 命中错误的行。焦点只能在这里自己补。
func (t *TUI) focusOnScrollTree(p scrollFocusable) {
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
		switch action {
		case tview.MouseScrollUp, tview.MouseScrollDown:
			if !p.HasFocus() {
				t.app.SetFocus(p)
			}
		}
		return action, event
	})
}

/** EVENT */

// quit 先停止 Loader，再等后台 goroutine 退出，最后停止事件循环。
// 不能在事件循环里同步等待，否则 Post 无法被消费。
func (t *TUI) quit() {
	if t.quitting {
		return
	}
	t.quitting = true

	// 先停掉动画，避免 ticker 在 app.Stop 之后继续投递更新。
	t.stopSyncSpinner()

	// Close 是非阻塞的，先阻止新的请求并取消在途 Load。
	t.closeLoaders()

	go func() {
		t.waitLoaders()
		t.app.Stop()
	}()
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

// focusChain 返回当前 Tab/Backtab 的循环顺序；history 弹窗显示时只在弹窗内部切换。
func (t *TUI) focusChain() []tview.Primitive {
	if t.historyVisible {
		return []tview.Primitive{t.historyList, t.historyDiff}
	}
	if t.activeTab == tabChange {
		return []tview.Primitive{t.filterText, t.branchList, t.changeList, t.changeDiff, t.commitSubject, t.commitDescription}
	}
	return []tview.Primitive{t.filterText, t.branchList, t.commitList, t.fileList, t.fileDiff}
}

// setFocusBorder 按焦点状态切换边框和标题颜色。
func (t *TUI) setFocusBorder(box *tview.Box, focused bool) {
	color := t.borderColor
	if focused {
		color = t.borderFocusColor
	}
	box.SetBorderColor(color).SetTitleColor(color)
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
	} else {
		t.reloadCommits()
	}

	// 数据可能命中 Loader 缓存而不会触发 changed 回调，
	// 这里主动按当前 tab 和最新 options 重渲染 diff。
	t.refreshActiveDiff()
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

	filter := ""
	if t.filterText != nil {
		filter = strings.ToLower(strings.TrimSpace(t.filterText.GetText()))
	}

	branchRoot := buildBranchTree(t.Branches, filter, t.HeadRef, t.syncFrame, t.remoteExpanded, t.headerStyle, t.headerSelectedStyle, t.selectedStyle)
	t.branchList.SetRoot(branchRoot)

	if node := t.branchNode(selected); node != nil {
		t.branchList.SetCurrentNode(node)
		return
	}
	// 默认选中第一个分支，而不是分组标题或 remote 目录。
	if node := firstBranchNode(branchRoot); node != nil {
		t.branchList.SetCurrentNode(node)
	}
}

// toggleRemoteFolder 切换 remote 目录的展开状态，并记住用户的选择以便重建分支树时恢复。
func (t *TUI) toggleRemoteFolder(node *tview.TreeNode) {
	expanded := !node.IsExpanded()
	setRemoteExpanded(node, expanded)
	if name, ok := node.GetReference().(string); ok {
		if t.remoteExpanded == nil {
			t.remoteExpanded = make(map[string]bool)
		}
		t.remoteExpanded[name] = expanded
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

	// 当前 commit 变化后回到默认的 first-parent 对比；compare 只对触发时的当前 commit 生效。
	if changed && t.CompareCommit != nil {
		t.setCompare(nil)
	}

	if force || changed {
		t.commitInfo.SetText(renderCommit(commit, t.infoWidth))
	}
	t.requestFiles(commit, t.CompareCommit)
}

// compareCommit 由 Ctrl+点击触发：把 index 对应的 commit 设为对比基准。
func (t *TUI) compareCommit(index int) {
	if t.commitList == nil || index < 0 || index >= len(t.Commits) {
		return
	}
	compare := t.Commits[index]

	// 当前 commit 不能和自己比较；再次点击已标记的 commit 视为取消对比。
	if compare.Hash == t.CurrentCommit.Hash ||
		(t.CompareCommit != nil && compare.Hash == t.CompareCommit.Hash) {
		if t.CompareCommit != nil {
			t.applyCompare(nil)
		}
		return
	}

	t.applyCompare(&compare)
}

// rebaseCommit 先弹窗确认，再把当前分支 rebase 到选中的 commit。
func (t *TUI) rebaseCommit() {
	if t.commitOp == nil || t.root == nil || t.promptVisible || t.commitOp.Loading() {
		return
	}
	row := t.commitList.currentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	message := fmt.Sprintf("Rebase %s onto %s?\n\n%s\n\n[gray]Local changes will be stashed and restored automatically.",
		tview.Escape(t.HeadRef), tview.Escape(commit.ShortHash()), tview.Escape(commit.Subject))

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Rebase"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.root.RemovePage("rebase")
			t.promptVisible = false
			t.app.SetFocus(t.commitList)
			if buttonIndex == 1 {
				t.executeCommitOp(commitOpRequest{op: opRebase, commit: commit})
			}
		})

	// Modal 默认用 ContrastBackgroundColor（蓝色）填充；跟随终端背景色。
	// Modal.SetBackgroundColor 只改内部的 frame/form，外层的 Box 需要单独设置。
	prompt.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	prompt.Box.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	// 高亮按钮默认是白底 + 终端默认前景色，在深色终端上文字会发白；
	// 固定成白底黑字，保证按钮文字始终是深色。
	prompt.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(tview.Styles.PrimaryTextColor).
		Foreground(tcell.ColorBlack))

	t.promptVisible = true
	t.root.AddPage("rebase", prompt, true, true)
	t.app.SetFocus(prompt)
}

// continueRebase 在冲突解决并暂存后继续 rebase。
func (t *TUI) continueRebase() {
	if t.commitOp == nil || t.commitOp.Loading() {
		return
	}
	t.executeCommitOp(commitOpRequest{op: opRebaseContinue})
}

// abortRebase 先弹窗确认，再取消未完成的 rebase。
func (t *TUI) abortRebase() {
	if t.commitOp == nil || t.root == nil || t.promptVisible || t.commitOp.Loading() {
		return
	}

	previous := t.app.GetFocus()

	prompt := tview.NewModal().
		SetText("Abort rebase?\n\nAll conflict resolutions will be lost.").
		AddButtons([]string{"Cancel", "Abort"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.root.RemovePage("rebase-abort")
			t.promptVisible = false
			if previous != nil {
				t.app.SetFocus(previous)
			} else {
				t.app.SetFocus(t.commitList)
			}
			if buttonIndex == 1 {
				t.executeCommitOp(commitOpRequest{op: opRebaseAbort})
			}
		})

	// Modal 默认用 ContrastBackgroundColor（蓝色）填充；跟随终端背景色。
	// Modal.SetBackgroundColor 只改内部的 frame/form，外层的 Box 需要单独设置。
	prompt.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	prompt.Box.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	// 高亮按钮默认是白底 + 终端默认前景色，在深色终端上文字会发白；
	// 固定成白底黑字，保证按钮文字始终是深色。
	prompt.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(tview.Styles.PrimaryTextColor).
		Foreground(tcell.ColorBlack))

	t.promptVisible = true
	t.root.AddPage("rebase-abort", prompt, true, true)
	t.app.SetFocus(prompt)
}

// resetCommit 先弹窗选择 reset 模式，确认后把当前分支重置到选中的 commit。
func (t *TUI) resetCommit() {
	if t.commitOp == nil || t.root == nil || t.promptVisible || t.commitOp.Loading() {
		return
	}
	row := t.commitList.currentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	message := fmt.Sprintf("Reset %s to %s?\n\n%s\n\n[gray]Soft[-] keeps index and working tree\n[gray]Mixed[-] resets index, keeps working tree\n[red]Hard[-] discards index and working tree changes",
		tview.Escape(t.HeadRef), tview.Escape(commit.ShortHash()), tview.Escape(commit.Subject))

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Soft", "Mixed", "Hard"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.root.RemovePage("reset")
			t.promptVisible = false
			t.app.SetFocus(t.commitList)
			switch buttonIndex {
			case 1:
				t.executeCommitOp(commitOpRequest{op: opReset, commit: commit, resetMode: core.ResetSoft})
			case 2:
				t.executeCommitOp(commitOpRequest{op: opReset, commit: commit, resetMode: core.ResetMixed})
			case 3:
				t.executeCommitOp(commitOpRequest{op: opReset, commit: commit, resetMode: core.ResetHard})
			}
		})

	// Modal 默认用 ContrastBackgroundColor（蓝色）填充；跟随终端背景色。
	// Modal.SetBackgroundColor 只改内部的 frame/form，外层的 Box 需要单独设置。
	prompt.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	prompt.Box.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	// 高亮按钮默认是白底 + 终端默认前景色，在深色终端上文字会发白；
	// 固定成白底黑字，保证按钮文字始终是深色。
	prompt.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(tview.Styles.PrimaryTextColor).
		Foreground(tcell.ColorBlack))

	t.promptVisible = true
	t.root.AddPage("reset", prompt, true, true)
	t.app.SetFocus(prompt)
}

// openFile 用系统默认应用打开当前选中的文件；目录、分组和占位节点忽略。
func (t *TUI) openFile(node *tview.TreeNode) {
	if node == nil {
		return
	}
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		return
	}
	if err := t.OpenFile(change.Path); err != nil {
		message := strings.Join(strings.Fields(err.Error()), " ")
		t.statusDone("open", "open failed: "+change.Path+": "+message)
		return
	}
	t.statusDone("open", "open "+change.Path)
}

// openHistory 弹出文件历史窗口；目录、分组和占位节点忽略。
func (t *TUI) openHistory(node *tview.TreeNode) {
	if t.root == nil || t.historyVisible || node == nil {
		return
	}
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		return
	}

	historyList := newListView(t.selectedStyle, tcell.StyleDefault)

	historyList.SetTitle(" history ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(historyList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(historyList.Box, false) })

	historyList.setSelectedFunc(func() {
		t.selectHistory()
	})
	historyList.setActivatedFunc(func() {
		t.locateHistory()
	})

	historyDiff := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetText("[gray]loading…[-]")

	historyDiff.SetTitle(" file diff: "+tview.Escape(change.Path)+" ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(historyDiff.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(historyDiff.Box, false) })

	historyDiff.SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
		t.syncHistoryDiffWidth(historyDiff)
		return historyDiff.GetInnerRect()
	})

	historyDiff.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'w':
			t.toggleWhitespace()
			return nil
		case 's':
			t.toggleSideBySide()
			return nil
		}
		return event
	})

	t.focusOnScroll(historyList)
	t.focusOnScroll(historyDiff)

	contentH := tview.NewFlex().
		AddItem(historyList, 40, 0, true).
		AddItem(historyDiff, 0, 1, false)

	contentV := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 3, 0, false).
		AddItem(contentH, 0, 1, true).
		AddItem(nil, 3, 0, false)

	// 弹窗铺满 mainPage 所在区域，左右各留 15 列、上下各留 3 行，
	// 内容区正好是 mainPage 宽 - 30、高 - 6。
	popup := tview.NewFlex().
		AddItem(nil, 15, 0, false).
		AddItem(contentV, 0, 1, true).
		AddItem(nil, 15, 0, false)

	// 弹窗铺满 mainPage，只有两个子面板需要接收鼠标；其余区域直接消费，
	// 避免点击穿透到下面的主界面。
	popup.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if historyList.InRect(event.Position()) || historyDiff.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	t.setFocusBorder(historyList.Box, true)
	t.setFocusBorder(historyDiff.Box, false)

	t.HistoryChange = change
	t.HistoryCommits = nil
	t.historyList = historyList
	t.historyDiff = historyDiff
	t.historyWidth = 0
	t.historyVisible = true
	t.root.AddPage("history", popup, true, true)
	t.app.SetFocus(historyList)

	t.requestHistory(change.Path)
}

// closeHistory 关闭文件历史弹窗，并把焦点还给当前 tab 的列表。
func (t *TUI) closeHistory() {
	if !t.historyVisible {
		return
	}
	t.historyVisible = false
	t.fileHistory.Cancel()
	t.fileHistoryDiff.Cancel()
	t.historyList = nil
	t.historyDiff = nil
	t.HistoryChange = core.FileChange{}
	t.HistoryCommits = nil
	t.root.RemovePage("history")
	if t.activeTab == tabChange {
		t.app.SetFocus(t.changeList)
	} else {
		t.app.SetFocus(t.fileList)
	}
}

// selectHistory 在历史列表中切换选中项时请求对应 commit 的文件 diff。
func (t *TUI) selectHistory() {
	if !t.historyVisible {
		return
	}
	index := t.historyList.currentItem()
	if index < 0 || index >= len(t.HistoryCommits) {
		return
	}
	t.requestHistoryDiff(t.HistoryCommits[index])
}

// locateHistory 关闭历史弹窗，切到 commit tab 并定位到选中的 commit。
func (t *TUI) locateHistory() {
	if !t.historyVisible {
		return
	}
	index := t.historyList.currentItem()
	if index < 0 || index >= len(t.HistoryCommits) {
		return
	}
	hash := t.HistoryCommits[index].Hash

	t.closeHistory()
	t.pendingCommit = hash
	if t.activeTab != tabCommit {
		t.switchTab(tabCommit)
	} else if row := t.commitRow(hash); row >= 0 {
		t.pendingCommit = ""
		t.commitList.setCurrentItem(row)
	} else {
		t.reloadCommits()
	}
	t.app.SetFocus(t.commitList)
}

func (t *TUI) selectFile(node *tview.TreeNode) {
	// 文件列表加载中时，旧树节点属于旧 commit，不能拿来请求新 commit 的 diff。
	if t.files.Loading() {
		return
	}
	t.requestFileDiff(t.CurrentCommit, t.CompareCommit, node)
}

func (t *TUI) selectLocalFile(node *tview.TreeNode) {
	// 列表加载中时，旧树节点属于旧数据，不能拿来发起新的 diff 请求。
	if t.localFiles.Loading() {
		return
	}
	t.requestLocalDiff(node)
}

// toggleLocalChange 用空格在暂存区和未暂存区之间移动当前文件。
func (t *TUI) toggleLocalChange() {
	if t.changeOp == nil || t.localFiles.Loading() || t.changeOp.Loading() {
		return
	}
	node := t.changeList.GetCurrentNode()
	if node == nil {
		return
	}

	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		// 目录节点：空格仍然展开/收起。
		if _, isFolder := node.GetReference().(string); isFolder {
			setFolderExpanded(node, !node.IsExpanded())
		}
		return
	}

	op := opStage
	if change.Scope == core.ScopeStaged {
		op = opUnstage
	}
	t.executeChangeOp(op, change)
}

// discardLocalChange 先弹窗确认，再丢弃当前文件的改动。
func (t *TUI) discardLocalChange() {
	if t.changeOp == nil || t.root == nil || t.promptVisible || t.localFiles.Loading() || t.changeOp.Loading() {
		return
	}
	node := t.changeList.GetCurrentNode()
	if node == nil {
		return
	}
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		return
	}

	var message string
	switch change.Scope {
	case core.ScopeStaged:
		message = fmt.Sprintf("Discard all changes to %s?\n\nThe file will be reset to HEAD. This cannot be undone.", tview.Escape(change.Path))
	case core.ScopeUntracked:
		message = fmt.Sprintf("Delete untracked file %s?\n\nThis cannot be undone.", tview.Escape(change.Path))
	default:
		message = fmt.Sprintf("Discard changes to %s?\n\nThis cannot be undone.", tview.Escape(change.Path))
	}

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Discard"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.root.RemovePage("discard")
			t.promptVisible = false
			t.app.SetFocus(t.changeList)
			if buttonIndex == 1 {
				t.executeChangeOp(opDiscard, change)
			}
		})

	// Modal 默认用 ContrastBackgroundColor（蓝色）填充；跟随终端背景色。
	// Modal.SetBackgroundColor 只改内部的 frame/form，外层的 Box 需要单独设置。
	prompt.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	prompt.Box.SetBackgroundColor(tview.Styles.PrimitiveBackgroundColor)

	// 高亮按钮默认是白底 + 终端默认前景色，在深色终端上文字会发白；
	// 固定成白底黑字，保证按钮文字始终是深色。
	prompt.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(tview.Styles.PrimaryTextColor).
		Foreground(tcell.ColorBlack))

	t.promptVisible = true
	t.root.AddPage("discard", prompt, true, true)
	t.app.SetFocus(prompt)
}

// toggleWhitespace 切换 diff 是否忽略纯空白改动，并重新加载当前 diff。
func (t *TUI) toggleWhitespace() {
	t.IgnoreWhitespace = !t.IgnoreWhitespace
	t.refreshViewedDiff()
}

// toggleSideBySide 切换 diff 是否使用左右对照布局，并重新加载当前 diff。
func (t *TUI) toggleSideBySide() {
	t.SideBySide = !t.SideBySide
	t.refreshViewedDiff()
}

// refreshViewedDiff 重新请求当前可见的 diff；history 弹窗打开时刷新弹窗内的 diff。
func (t *TUI) refreshViewedDiff() {
	if t.historyVisible {
		t.refreshHistoryDiff()
		return
	}
	t.refreshActiveDiff()
}

// refreshActiveDiff 重新请求当前 tab 中选中文件的 diff。
func (t *TUI) refreshActiveDiff() {
	if t.activeTab == tabChange {
		if t.changeList == nil || (t.localFiles != nil && t.localFiles.Loading()) {
			return
		}
		if node := t.changeList.GetCurrentNode(); node != nil {
			if _, ok := node.GetReference().(core.FileChange); ok {
				t.requestLocalDiff(node)
			}
		}
		return
	}

	if t.fileList == nil || (t.files != nil && t.files.Loading()) {
		return
	}
	if node := t.fileList.GetCurrentNode(); node != nil {
		if _, ok := node.GetReference().(core.FileChange); ok {
			t.requestFileDiff(t.CurrentCommit, t.CompareCommit, node)
		}
	}
}

// refreshHistoryDiff 按当前选中的历史 commit 重新请求 diff。
func (t *TUI) refreshHistoryDiff() {
	index := t.historyList.currentItem()
	if index >= 0 && index < len(t.HistoryCommits) {
		t.requestHistoryDiff(t.HistoryCommits[index])
	}
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
	t.executeCheckout(node)
}

// syncBranch 拉取当前分支、推送当前分支或拉取远端引用；同一时间只允许一个同步操作。
func (t *TUI) syncBranch(op syncOp) {
	if t.syncOp == nil || t.syncOp.Loading() {
		return
	}
	t.executeSync(op)
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

	t.Branches = refs.Branches
	t.HeadRef = refs.HeadRef
	t.HeadHash = refs.HeadHash
	t.Rebasing = refs.Rebasing
	t.renderRebaseActions()
	if t.repoHead != nil {
		t.repoHead.SetText(t.HeadRef)
	}
	t.refreshBranches(selected)
}

// branchNode 返回分支 name 对应的节点，本地和远端都会匹配；name 为空或不存在时返回 nil。
func (t *TUI) branchNode(name string) *tview.TreeNode {
	if t.branchList == nil {
		return nil
	}
	return findBranchNode(t.branchList.GetRoot(), name)
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

	target := previous
	if t.pendingCommit != "" {
		target = t.pendingCommit
		t.pendingCommit = ""
	}

	t.Commits = commits
	t.commitList.setLines(renderGraph(commits, core.ComputeGraph(commits)))
	// 刷新后 compare 可能已不存在（rebase/切分支），按 hash 重新确认。
	t.syncCompare()

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

	if found := t.commitRow(target); found >= 0 {
		row = found
	} else if found := t.commitRow(previous); found >= 0 {
		row = found
	}
	// setCurrentItem 会像用户选中一样触发 refreshCommit，这里不再额外请求。
	t.commitList.setCurrentItem(row)
}

// applyCompare 设置对比基准并重新加载当前 commit 的文件列表。
func (t *TUI) applyCompare(commit *core.Commit) {
	t.setCompare(commit)
	if t.CurrentCommit.Hash != "" {
		t.requestFiles(t.CurrentCommit, t.CompareCommit)
	}
}

// syncCompare 在提交列表重建后按 hash 重新定位 compare；commit 已不存在时清除。
func (t *TUI) syncCompare() {
	if t.CompareCommit == nil {
		return
	}
	if t.commitRow(t.CompareCommit.Hash) < 0 {
		t.setCompare(nil)
		return
	}
	t.setCompare(t.CompareCommit)
}

// setCompare 更新对比基准和列表标记，不发起文件/diff 请求。
func (t *TUI) setCompare(commit *core.Commit) {
	t.CompareCommit = commit

	row := -1
	if commit != nil {
		row = t.commitRow(commit.Hash)
	}
	if t.commitList != nil {
		t.commitList.setMarkedItem(row)
	}
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

// syncCommitInfoWidth 在 commit 信息面板宽度变化时重排文本。
func (t *TUI) syncCommitInfoWidth(view *tview.TextView) {
	_, _, width, _ := view.GetInnerRect()
	if width <= 0 || width == t.infoWidth {
		return
	}
	t.infoWidth = width
	// 宽度变化只需重排文本，不修改选择状态。
	view.SetText(renderCommit(t.CurrentCommit, width))
}

/** FILES */

// requestFiles 请求 commit 相对 compare 的文件列表；相同 commit+compare 的重复请求由 Loader 去重。
func (t *TUI) requestFiles(commit core.Commit, compare *core.Commit) {
	t.files.Request(filesRequest{commit: commit, compare: compare})
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

func (t *TUI) requestFileDiff(commit core.Commit, compare *core.Commit, node *tview.TreeNode) {
	change, ok := node.GetReference().(core.FileChange)
	if !ok {
		// 目录节点提示选择文件；错误/无变更占位节点保留当前内容
		if _, isFolder := node.GetReference().(string); isFolder {
			t.clearFileDiff("select a file")
		}
		return
	}
	t.diff.Request(diffRequest{
		commit:  commit,
		change:  change,
		compare: compare,
		options: t.diffOptions(t.fileDiff),
	})
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
	t.localDiff.Request(localDiffRequest{
		change:  change,
		options: t.diffOptions(t.changeDiff),
	})
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

/** HISTORY */

// requestHistory 请求 path 的文件历史；相同路径的重复请求由 Loader 去重。
func (t *TUI) requestHistory(path string) {
	t.fileHistory.Request(historyRequest{path: path})
}

// showHistory 用文件历史填充弹窗左侧列表，默认选中第一条并请求 diff。
func (t *TUI) showHistory(_ historyRequest, commits []core.Commit, err error) {
	if !t.historyVisible || t.historyList == nil || t.historyDiff == nil {
		return
	}
	t.HistoryCommits = commits

	switch {
	case err != nil:
		t.historyList.SetTitle(" history ")
		t.historyList.setItems(nil)
		t.fileHistoryDiff.Cancel()
		t.historyDiff.SetText("[red]failed to load history:[-] " + tview.Escape(err.Error()))
		return
	case len(commits) == 0:
		t.historyList.SetTitle(" history (0) ")
		t.historyList.setItems(nil)
		t.fileHistoryDiff.Cancel()
		t.historyDiff.SetText("[gray](no history)[-]")
		return
	}

	t.historyList.SetTitle(fmt.Sprintf(" history (%d) ", len(commits)))
	items := make([][]string, 0, len(commits))
	for _, commit := range commits {
		header := fmt.Sprintf("[aqua]%s[-] [gray]%s[-]", tview.Escape(commit.Author), commit.Date.Format("Jan _2, 2006 15:04:05"))
		subject := " [::b]" + tview.Escape(commit.Subject) + "[-::-]"
		items = append(items, []string{header, subject})
	}
	t.historyList.setItems(items)
	t.historyList.setCurrentItem(0)
}

/** HISTORY DIFF */

func (t *TUI) requestHistoryDiff(commit core.Commit) {
	if t.historyDiff == nil {
		return
	}
	options := core.DiffOptions{
		IgnoreWhitespace: t.IgnoreWhitespace,
		SideBySide:       t.SideBySide,
	}
	if options.SideBySide {
		if _, _, width, _ := t.historyDiff.GetInnerRect(); width > 0 {
			t.historyWidth = width
		}
		options.Width = t.historyWidth
	}
	t.fileHistoryDiff.Request(historyDiffRequest{
		commit:  commit,
		change:  core.FileChange{Path: t.HistoryChange.Path},
		options: options,
	})
}

func (t *TUI) showHistoryDiff(req historyDiffRequest, text string, err error) {
	if !t.historyVisible || t.historyDiff == nil {
		return
	}
	switch {
	case err != nil:
		text = "[red]error[-] " + tview.Escape(err.Error())
	case text == "":
		text = "[gray](no diff)[-]"
	}
	t.historyDiff.SetTitle(" file diff: " + tview.Escape(req.change.Path) + " ")
	t.historyDiff.SetText(text)
	t.historyDiff.ScrollToBeginning()
}

// syncHistoryDiffWidth 在 side-by-side 模式下按弹窗内宽重新请求 diff。
func (t *TUI) syncHistoryDiffWidth(view *tview.TextView) {
	if !t.historyVisible || !t.SideBySide {
		return
	}
	_, _, width, _ := view.GetInnerRect()
	if width <= 0 || width == t.historyWidth {
		return
	}
	t.historyWidth = width
	t.refreshHistoryDiff()
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

// diffView 返回当前显示的 diff 面板；history 弹窗打开时返回弹窗内的 diff。
func (t *TUI) diffView() *tview.TextView {
	if t.historyVisible {
		return t.historyDiff
	}
	if t.activeTab == tabChange {
		return t.changeDiff
	}
	return t.fileDiff
}

// diffOptions 按当前状态和 diff 面板宽度动态组装展示选项。
func (t *TUI) diffOptions(view *tview.TextView) core.DiffOptions {
	options := core.DiffOptions{
		IgnoreWhitespace: t.IgnoreWhitespace,
		SideBySide:       t.SideBySide,
	}
	if t.SideBySide && view != nil {
		if _, _, width, _ := view.GetInnerRect(); width > 0 {
			t.diffWidth = width
		}
		options.Width = t.diffWidth
	}
	return options
}

// drawDiffBadges 在当前 diff 面板标题右侧画加粗的 W / S 提示。
func (t *TUI) drawDiffBadges(screen tcell.Screen) {
	view := t.diffView()
	if view == nil {
		return
	}

	var parts []string
	if !t.IgnoreWhitespace {
		parts = append(parts, "W")
	}
	if t.SideBySide {
		parts = append(parts, "S")
	}
	if len(parts) == 0 {
		return
	}
	badge := " " + strings.Join(parts, "") + " "

	x, y, width, _ := view.GetRect()
	// 终端太窄时不画，避免覆盖左侧 " file diff: path " 标题。
	if width-2 < tview.TaggedStringWidth(view.GetTitle())+len(badge)+1 {
		return
	}

	start := x + width - len(badge) - 1
	for i, r := range badge {
		_, _, style, _ := screen.GetContent(start+i, y)
		screen.SetContent(start+i, y, r, nil, style.Bold(true))
	}
}

// syncDiffViewWidth 在 side-by-side 模式下按当前面板内宽重新请求 diff。
// 由 diff 面板的 DrawFunc 调用，此时 Flex 已设置好本帧的 rect。
func (t *TUI) syncDiffViewWidth(view *tview.TextView) {
	if !t.SideBySide {
		return
	}
	_, _, width, _ := view.GetInnerRect()
	if width <= 0 || width == t.diffWidth {
		return
	}
	t.diffWidth = width
	t.refreshActiveDiff()
}

/** CHANGE OP */

func (t *TUI) executeChangeOp(op changeOp, change core.FileChange) {
	t.changeOp.Reload(changeOpRequest{op: op, change: change})
}

func (t *TUI) showChangeOpLoading(req changeOpRequest) {
	t.statusStart("change", t.changeOpLabel(req, false))
}

func (t *TUI) showChangeOp(req changeOpRequest, _ string, err error) {
	message := t.changeOpLabel(req, true)
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message += " failed: " + strings.Join(strings.Fields(err.Error()), " ")
		t.statusDone("change", message)
		return
	}
	t.statusDone("change", message)
	t.reloadLocal()
}

// changeOpLabel 返回本地变更操作的 loading/完成文案。
func (t *TUI) changeOpLabel(req changeOpRequest, done bool) string {
	switch req.op {
	case opStage:
		if done {
			return "staged " + req.change.Path
		}
		return "staging " + req.change.Path
	case opUnstage:
		if done {
			return "unstaged " + req.change.Path
		}
		return "unstaging " + req.change.Path
	default:
		if done {
			return "discarded " + req.change.Path
		}
		return "discarding " + req.change.Path
	}
}

/** CHECKOUT */

// executeCheckout 通过节点引用取分支并绕过缓存，保证每次按空格都真正执行 git checkout。
func (t *TUI) executeCheckout(node *tview.TreeNode) {
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

/** SYNC */

// executeSync 通过 Reload 绕过缓存，保证每次按键都真正执行 git 命令。
func (t *TUI) executeSync(op syncOp) {
	t.syncOp.Reload(syncOpRequest{op: op})
}

func (t *TUI) showSyncLoading(req syncOpRequest) {
	t.statusStart("sync", t.syncOpLabel(req, false))
	t.startSyncSpinner()
}

func (t *TUI) showSync(req syncOpRequest, _ string, err error) {
	t.stopSyncSpinner()

	message := t.syncOpLabel(req, true)
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message = strings.Join(strings.Fields(err.Error()), " ")
		switch req.op {
		case opPull:
			message = "pull failed: " + message
		case opPush:
			message = "push failed: " + message
		default:
			message = "fetch failed: " + message
		}
	}
	t.statusDone("sync", message)

	// pull 失败也可能留下冲突或已更新的 refs，统一按当前 tab 刷新。
	t.refreshTab(t.activeTab)
}

// syncOpLabel 返回同步操作的 loading/完成文案。
func (t *TUI) syncOpLabel(req syncOpRequest, done bool) string {
	switch req.op {
	case opPull:
		if done {
			return "pulled " + t.HeadRef
		}
		return "pull " + t.HeadRef
	case opPush:
		if done {
			return "pushed " + t.HeadRef
		}
		return "push " + t.HeadRef
	default:
		if done {
			return "fetched"
		}
		return "fetch"
	}
}

/** COMMIT OP */

// executeCommitOp 通过 Reload 绕过缓存，保证每次选择都真正执行 git 操作。
func (t *TUI) executeCommitOp(req commitOpRequest) {
	t.commitOp.Reload(req)
}

func (t *TUI) showCommitOpLoading(req commitOpRequest) {
	t.statusStart("commit", t.commitOpLabel(req, false))
}

func (t *TUI) showCommitOp(req commitOpRequest, _ string, err error) {
	message := t.commitOpLabel(req, true)
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message = strings.Join(strings.Fields(err.Error()), " ")
		switch req.op {
		case opReset:
			message = "reset failed: " + message
		case opRebaseContinue:
			message = "rebase continue failed: " + message
		case opRebaseAbort:
			message = "rebase abort failed: " + message
		default:
			message = "rebase failed: " + message
		}
	}
	t.statusDone("commit", message)

	// reset/rebase 会移动 HEAD，并可能改变索引和工作区；rebase 冲突时还会留下中间状态。
	t.refreshTab(t.activeTab)
}

// commitOpLabel 返回 commit 操作的 loading/完成文案。
func (t *TUI) commitOpLabel(req commitOpRequest, done bool) string {
	switch req.op {
	case opReset:
		if done {
			return "reset --" + req.resetMode.String() + " " + req.commit.ShortHash()
		}
		return "resetting --" + req.resetMode.String() + " " + req.commit.ShortHash()
	case opRebaseContinue:
		if done {
			return "rebase continued"
		}
		return "continuing rebase"
	case opRebaseAbort:
		if done {
			return "rebase aborted"
		}
		return "aborting rebase"
	default:
		if done {
			return "rebased onto " + req.commit.ShortHash()
		}
		return "rebasing onto " + req.commit.ShortHash()
	}
}
