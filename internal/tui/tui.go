package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lizzz0523/lanegraph/internal/async"
	"github.com/lizzz0523/lanegraph/internal/core"

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
	refList    *tview.TreeView
	mainPanels *tview.Pages
	statusBar  *tview.TextView
	statusRow  *tview.Flex
	// keybindHint 是状态栏右侧的 “Keybindings: ?” 提示，和 Continue/Abort 互斥显示。
	keybindHint *tview.TextView
	// continueButton / abortButton 按当前未完成的操作（rebase / merge / squash / cherry-pick）显示对应文案和动作；
	// 没有未完成操作时宽度为 0。
	continueButton *tview.Button
	abortButton    *tview.Button

	commitPanel *tview.Flex
	commitList  *listView
	commitInfo  *tview.TextView
	fileList    *tview.TreeView
	fileDiff    *diffView

	changePanel       *tview.Flex
	changeList        *tview.TreeView
	changeDiff        *diffView
	commitSubject     *tview.TextArea
	commitDescription *tview.TextArea
	commitButton      *tview.Button
	amendCheck        *tview.Checkbox

	rebaseForm  *rebaseForm     // interactive rebase 弹窗
	rewordForm  *rewordForm     // 单条 commit reword 弹窗
	historyView *historyView    // 文件历史弹窗
	returnFocus tview.Primitive // 打开弹窗前聚焦的控件，关闭弹窗后恢复

	tabStyle            tcell.Style
	tabActiveStyle      tcell.Style
	selectedStyle       tcell.Style
	headerStyle         tcell.Style
	headerSelectedStyle tcell.Style
	borderColor         tcell.Color
	borderFocusColor    tcell.Color

	activeTab         string // 当前显示的主面板 tab
	infoWidth         int
	diffWidth         int               // 当前 diff 渲染使用的面板内宽
	pendingHead       bool              // pull/checkout 后等待 refs 刷新拿到新 HeadHash 再选中 HEAD 提交
	pendingRef        string            // checkout 后等待 refs 刷新完成再选中的引用
	pendingCommit     string            // history 跳转后等待 commits 刷新完成再选中的提交
	refFolderExpanded map[string]bool   // 用户手动展开过的引用树目录，按目录 key 记录
	rebasePopup       *rebasePopup      // interactive rebase 的业务状态
	rewordPopup       *rewordPopup      // 单条 commit reword 的业务状态
	historyPopup      *historyPopup     // 文件历史弹窗的业务状态
	syncFrame         int               // 分支同步动画当前帧；-1 表示没有在执行同步操作
	syncStop          chan struct{}     // 关闭后停止分支同步动画
	statusMsg         string            // 最近一次操作结果，进行中项清空后保留
	statusBusy        map[string]string // statusBar 当前展示的进行中项，key 为槽位
	helpVisible       bool              // 快捷键帮助弹窗是否正在显示
	promptVisible     bool              // 确认弹窗是否正在显示
	rebaseVisible     bool              // interactive rebase 弹窗是否正在显示
	rewordVisible     bool              // 单条 commit reword 弹窗是否正在显示
	historyVisible    bool              // 文件历史弹窗是否正在显示
	commitInfoVisible bool              // commit 详情弹窗是否正在显示
	quitting          bool              // 退出流程已启动，忽略重复退出

	tasks tasks
}

func New(st *core.State) *TUI {
	return &TUI{State: st}
}

/** RUN */

func (t *TUI) Run() error {
	app := tview.NewApplication()

	base, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	screen := &tuiScreen{Screen: base, app: app}

	// SetScreen 必须在 EnableMouse 之前：预先设置的 screen 不会在 Run 中再开启鼠标。
	app.SetScreen(screen).
		SetBeforeDrawFunc(screen.beforeDraw).
		SetRoot(t.setupUI(), true).
		EnableMouse(true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// 弹窗显示时，Esc/q 只关闭弹窗；Ctrl+Q 仍然随时可退出。
		// Tab/Backtab 在弹窗内部循环焦点。
		if t.helpVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				t.closeHelp()
				return nil
			case tcell.KeyRune:
				if event.Rune() == keymap.dialogClose || event.Rune() == keymap.help {
					t.closeHelp()
					return nil
				}
			case tcell.KeyTab, tcell.KeyBacktab:
				return nil
			}
		}

		if t.rebaseVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				t.closeRebaseInteractive()
				return nil
			case tcell.KeyRune:
				if event.Rune() == keymap.dialogClose {
					t.closeRebaseInteractive()
					return nil
				}
			case tcell.KeyTab:
				app.SetFocus(t.nextFocus(1))
				return nil
			case tcell.KeyBacktab:
				app.SetFocus(t.nextFocus(-1))
				return nil
			}
		}

		if t.rewordVisible {
			editing := t.rewordForm.subject.HasFocus() || t.rewordForm.description.HasFocus()
			switch event.Key() {
			case tcell.KeyEscape:
				t.closeReword()
				return nil
			case tcell.KeyRune:
				if event.Rune() == keymap.dialogClose && !editing {
					t.closeReword()
					return nil
				}
			case tcell.KeyTab:
				app.SetFocus(t.nextFocus(1))
				return nil
			case tcell.KeyBacktab:
				app.SetFocus(t.nextFocus(-1))
				return nil
			}
		}

		if t.historyVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				t.closeHistory()
				return nil
			case tcell.KeyRune:
				if event.Rune() == keymap.dialogClose {
					t.closeHistory()
					return nil
				}
			case tcell.KeyTab:
				app.SetFocus(t.nextFocus(1))
				return nil
			case tcell.KeyBacktab:
				app.SetFocus(t.nextFocus(-1))
				return nil
			}
		}

		if t.commitInfoVisible {
			switch event.Key() {
			case tcell.KeyEscape:
				t.closeCommitInfo()
				return nil
			case tcell.KeyRune:
				if event.Rune() == keymap.dialogClose {
					t.closeCommitInfo()
					return nil
				}
			case tcell.KeyTab, tcell.KeyBacktab:
				return nil
			}
		}

		filtering := t.filterText != nil && t.filterText.HasFocus()
		committing := (t.commitSubject != nil && t.commitSubject.HasFocus()) ||
			(t.commitDescription != nil && t.commitDescription.HasFocus())
		// filter 和 commit 输入框里要能正常输入字母；Ctrl+Q 仍然随时可退出。
		typing := filtering || committing
		switch {
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == keymap.quit && !typing && !t.popupVisible()):
			t.quit()
			return nil
		case event.Key() == tcell.KeyEscape && filtering:
			t.filterText.SetText("", false)
			app.SetFocus(t.refList)
			return nil
		// commit 输入框里 Esc 回到 local changes 列表。
		case event.Key() == tcell.KeyEscape && committing:
			app.SetFocus(t.changeList)
			return nil
		case event.Key() == tcell.KeyTab && !t.promptVisible && !t.commitInfoVisible:
			app.SetFocus(t.nextFocus(1))
			return nil
		case event.Key() == tcell.KeyBacktab && !t.promptVisible && !t.commitInfoVisible:
			app.SetFocus(t.nextFocus(-1))
			return nil
		// p / P / f 全局触发当前分支的 pull / push / fetch；
		// B / T 全局从当前 HEAD 创建并检出分支 / 创建标签；输入框输入时不拦截。
		case event.Key() == tcell.KeyRune && !typing && !t.popupVisible():
			switch event.Rune() {
			case keymap.help:
				t.openHelp()
				return nil
			case keymap.globalPull:
				t.syncBranch(opPull)
				return nil
			case keymap.globalPush:
				t.syncBranch(opPush)
				return nil
			case keymap.globalFetch:
				t.syncBranch(opFetch)
				return nil
			case keymap.globalCreateBranch:
				t.createRef(opCreateBranch, nil)
				return nil
			case keymap.globalCreateTag:
				t.createRef(opCreateTag, nil)
				return nil
			}
		}
		return event
	})

	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		t.drawDiffBadges(screen)
	})

	t.app = app

	t.setupTasks()
	defer t.tasks.close()

	t.refreshTabStyles()
	t.refreshRefs("")
	t.refreshCommit(true)

	return app.Run()
}

// drawInterval 是两次整屏绘制之间的最短间隔，约一帧。
const drawInterval = 16 * time.Millisecond

// tuiScreen 限制整屏绘制的频率。tview 每处理一个鼠标事件都会同步整屏重绘，
// 快速滚轮时事件产生得比绘制快，在队列里积压，反向滚动要等旧事件处理完才有反应。
// 合并成每帧最多绘制一次后，积压的事件只改滚动位置，队列很快就能排空。
// 刷新前还会修正压在弹窗边框上的宽字符，见 fixWideOverlaps。
type tuiScreen struct {
	tcell.Screen

	app     *tview.Application
	pending bool      // 已安排补画，期间不再重复排队
	skipped bool      // 本次绘制被跳过，紧接着的 Show 也要跳过
	last    time.Time // 上次整屏绘制的时间
}

// beforeDraw 作为 BeforeDrawFunc：距上次绘制不足一帧时跳过本次绘制，并安排到期后补画。
// 它和 Show 都只在主循环里调用，字段无需加锁。
func (s *tuiScreen) beforeDraw(tcell.Screen) bool {
	if wait := drawInterval - time.Since(s.last); wait > 0 {
		s.skipped = true
		if !s.pending {
			s.pending = true
			time.AfterFunc(wait, func() {
				s.app.QueueUpdateDraw(func() { s.pending = false })
			})
		}
		return true
	}
	s.last = time.Now()
	return false
}

// Show 在跳过绘制时不刷新：tview 绘制前已清空缓冲，此时刷新会闪出空白。
func (s *tuiScreen) Show() {
	if s.skipped {
		s.skipped = false
		return
	}
	s.fixWideOverlaps()
	s.Screen.Show()
}

// fixWideOverlaps 让被弹窗压住半个格子的宽字符让位。
// tcell 画宽字符时会连它的第二格一起跳过（tScreen.draw 里 x += width-1），
// 第二格如果被后画的弹窗边框占用，这一格就永远发不到终端，看起来像边框缺了一段。
// tview 画出来的宽字符第二格都是空格，所以第二格不是空格时就是被上层控件覆盖了。
func (s *tuiScreen) fixWideOverlaps() {
	width, height := s.Size()
	for y := 0; y < height; y++ {
		for x := 0; x < width-1; x++ {
			_, _, style, cellWidth := s.GetContent(x, y)
			if cellWidth < 2 {
				continue
			}
			if mainc, _, _, _ := s.GetContent(x+1, y); mainc != ' ' {
				s.SetContent(x, y, ' ', nil, style)
			}
		}
	}
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
	tview.Styles.PrimitiveBackgroundColor = colorBackground.cell

	// 应用没有双击语义；tview 默认按 500ms 判定双击，
	// 快速点击两个不同的 commit 时第二次点击会被当成双击吞掉。
	tview.DoubleClickInterval = 0

	defaultStyle := tcell.StyleDefault.
		Foreground(colorMuted.cell).
		Background(colorBackground.cell)

	// 选中态用灰底代替 tview 默认的白底反色；前景色与未选中行一致
	selectedStyle := tcell.StyleDefault.
		Foreground(colorText.cell).
		Background(colorSelection.cell)

	// 对比基准行用深青色背景，和当前选中行的灰色区分。
	highlightStyle := tcell.StyleDefault.
		Foreground(colorText.cell).
		Background(colorHighlight.cell)

	// 强调色用和边框高亮一致的 aqua 背景，配黑字保证对比度；
	// 激活 tab 和 statusBar 上的主操作按钮共用。
	accentStyle := tcell.StyleDefault.
		Foreground(colorOnAccent.cell).
		Background(colorAccent.cell)

	// 危险操作用红字，配终端默认背景。
	dangerStyle := tcell.StyleDefault.
		Foreground(colorDanger.cell).
		Background(colorBackground.cell)

	headerStyle := tcell.StyleDefault.
		Foreground(colorMuted.cell).
		Background(colorTransparent.cell).
		Bold(true)

	headerSelectedStyle := tcell.StyleDefault.
		Foreground(colorMuted.cell).
		Background(colorTransparent.cell).
		Bold(true)

	commitList := newListView(selectedStyle, highlightStyle)

	commitList.SetTitle(" all commits ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitList.Box, false) })

	// R 把当前分支 reset 到选中的 commit；
	// r 把当前分支 rebase 到选中的 commit；
	// i 执行 interactive rebase；
	// w 改写选中的 commit 的提交信息；
	// m 把选中的 commit merge 到当前分支；
	// c 把选中的 commit cherry-pick 到当前分支；
	// b / t 从选中的 commit 创建分支 / 标签。
	commitList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case keymap.commitParent:
				t.jumpToParent()
				return nil
			case keymap.commitChild:
				t.jumpToChild()
				return nil
			case keymap.commitReset:
				t.resetCommit()
				return nil
			case keymap.commitRebase:
				t.rebaseCommit()
				return nil
			case keymap.commitInteractiveRebase:
				t.rebaseCommitInteractive()
				return nil
			case keymap.commitReword:
				t.rewordCommit()
				return nil
			case keymap.commitMerge:
				t.mergeCommit()
				return nil
			case keymap.commitCherryPick:
				t.cherryPickCommit()
				return nil
			case keymap.commitBranch:
				t.createRefFromCommit(opCreateBranch)
				return nil
			case keymap.commitTag:
				t.createRefFromCommit(opCreateTag)
				return nil
			}
		}
		return event
	})

	commitList.SetLines(renderGraph(t.Commits, core.ComputeGraph(t.Commits), true))

	commitList.SetSelectedFunc(func() {
		t.refreshCommit(false)
	})
	commitList.SetHighlightFunc(func(index int) {
		t.compareCommit(index)
	})

	commitInfo := tview.NewTextView().
		SetWrap(false).
		SetDynamicColors(true)

	commitInfo.SetTitle(" commit ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitInfo.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitInfo.Box, false) }).
		SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
			t.syncCommitInfoWidth(commitInfo)
			return commitInfo.GetInnerRect()
		})

	// 空格弹窗查看完整的 commit 信息。
	commitInfo.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune && event.Rune() == keymap.commitInfo {
			t.openCommitInfo()
			return nil
		}
		return event
	})

	fileList := tview.NewTreeView().
		SetRoot(buildFileTree(nil, selectedStyle)).
		SetTopLevel(1).
		SetGraphics(false)

	fileList.SetTitle(" commit files ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(fileList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(fileList.Box, false) })

	// 本次事件开始前的当前节点：只有已经是选中项的目录再次被选中时才展开/收起。
	var filePrevCurrent *tview.TreeNode

	// o 用系统默认应用打开选中的文件，h 查看文件历史。
	fileList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		filePrevCurrent = fileList.GetCurrentNode()
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case keymap.fileOpen:
			t.openFile(fileList.GetCurrentNode())
			return nil
		case keymap.fileHistory:
			t.openHistory(fileList.GetCurrentNode())
			return nil
		}
		return event
	})

	t.focusOnScrollTree(fileList, func() { filePrevCurrent = fileList.GetCurrentNode() })

	// 文件预览由 changed 负责，这里只处理目录展开/收起；
	// 鼠标第一次点中目录只移动光标，再次点击已选中的目录才切换。
	fileList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.tasks.files.Running() || len(node.GetChildren()) == 0 || node != filePrevCurrent {
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	fileList.SetChangedFunc(func(node *tview.TreeNode) {
		t.selectFile(node)
	})

	fileDiff := newDiffView()
	fileDiff.SetText(colorMuted.markup("select a file"))

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
		case keymap.diffWhitespace:
			t.toggleWhitespace()
			return nil
		case keymap.diffSideBySide:
			t.toggleSideBySide()
			return nil
		}
		return event
	})

	// 在控件绘制前按当前内宽重渲染 diff；此时 Flex 已设置本帧的 rect。
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

	// 本次事件开始前的当前节点：只有已经是选中项的目录再次被选中时才展开/收起。
	var changePrevCurrent *tview.TreeNode

	// 空格 stage/unstage 当前文件或目录内的全部变更，d 丢弃文件改动，o 打开文件，h 查看文件历史；
	// 目录的展开/收起用 Enter。
	changeList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		changePrevCurrent = changeList.GetCurrentNode()
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case keymap.changeStage:
			t.toggleLocalChange()
			return nil
		case keymap.changeDiscard:
			t.discardLocalChange()
			return nil
		case keymap.fileOpen:
			t.openFile(changeList.GetCurrentNode())
			return nil
		case keymap.fileHistory:
			t.openHistory(changeList.GetCurrentNode())
			return nil
		}
		return event
	})

	t.focusOnScrollTree(changeList, func() { changePrevCurrent = changeList.GetCurrentNode() })

	// 文件预览由 changed 负责，这里只处理目录展开/收起；
	// 鼠标第一次点中目录只移动光标，再次点击已选中的目录才切换。
	changeList.SetSelectedFunc(func(node *tview.TreeNode) {
		if t.tasks.localFiles.Running() || len(node.GetChildren()) == 0 || node != changePrevCurrent {
			return
		}
		setFolderExpanded(node, !node.IsExpanded())
	})

	changeList.SetChangedFunc(func(node *tview.TreeNode) {
		t.selectLocalFile(node)
	})

	changeDiff := newDiffView()
	changeDiff.SetText(colorMuted.markup("select a file"))

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
		case keymap.diffWhitespace:
			t.toggleWhitespace()
			return nil
		case keymap.diffSideBySide:
			t.toggleSideBySide()
			return nil
		}
		return event
	})

	// 在控件绘制前按当前内宽重渲染 diff；此时 Flex 已设置本帧的 rect。
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
		SetActivatedStyle(accentStyle).
		SetSelectedFunc(func() { t.commitChanges() })

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
		t.refreshRefs(t.refName())
	})

	refList := tview.NewTreeView().
		SetTopLevel(1).
		SetGraphics(false)

	refList.SetTitle(" branches ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(refList.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(refList.Box, false) })

	// 空格 checkout 当前分支或标签，d 删除引用，目录切换展开状态。
	// TreeView 默认把空格当选中，必须在这里先截获。
	refList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		node := refList.GetCurrentNode()
		if node == nil {
			return event
		}
		switch event.Rune() {
		case keymap.refCheckout:
			if _, isFolder := node.GetReference().(folder); isFolder {
				t.toggleRefFolder(node)
			} else {
				t.checkoutRef(node)
			}
			return nil
		case keymap.refDelete:
			t.deleteRef(node)
			return nil
		}
		return event
	})

	// 回车或鼠标点击目录时切换展开状态，点击引用时跳转到对应 commit。
	refList.SetSelectedFunc(func(node *tview.TreeNode) {
		if _, isFolder := node.GetReference().(folder); isFolder {
			t.toggleRefFolder(node)
			return
		}
		// commits 刷新后行号会变化，每次跳转都按当前列表查询。
		if row := t.commitRow(refHash(node)); row >= 0 {
			t.commitList.SetCurrentItem(row)
		}
	})

	t.focusOnScrollTree(refList, nil)

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
			commitList.SetCurrentItem(row)
		}
		if node := t.refNode(t.HeadRef); node != nil {
			refList.SetCurrentNode(node)
		}
		t.app.SetFocus(refList)
		return tview.MouseConsumed, nil
	})

	changeTab := tview.NewButton("Local Changes")
	commitTab := tview.NewButton("All Commits")

	// 按钮获得焦点时按 Enter 切换 tab；再次按下当前 tab 会刷新数据。
	changeTab.SetSelectedFunc(func() { t.switchTab(tabChange) })
	commitTab.SetSelectedFunc(func() { t.switchTab(tabCommit) })

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
		AddItem(refList, 0, 1, false)

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

	// rebase / merge / cherry-pick 未完成时，在 statusBar 右侧显示 Continue / Abort；
	// 文案和动作由 renderInProgressActions 按当前状态切换，平时宽度为 0，不占用状态栏空间。
	continueButton := tview.NewButton("")
	continueButton.SetStyle(accentStyle).SetActivatedStyle(accentStyle)
	bindStatusButton(continueButton)

	abortButton := tview.NewButton("")
	abortButton.SetStyle(dangerStyle).SetActivatedStyle(dangerStyle)
	bindStatusButton(abortButton)

	// 右侧平时显示快捷键入口，有未完成操作时让位给 Continue/Abort。
	hintText := "Keybindings: ?"
	hintWidth := tview.TaggedStringWidth(hintText) + 2

	keybindHint := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false).
		SetTextAlign(tview.AlignRight).
		SetText(colorAccent.markup(hintText))
	keybindHint.SetBorderPadding(0, 0, 1, 1)

	statusRow := tview.NewFlex().
		AddItem(statusBar, 0, 1, false).
		AddItem(keybindHint, hintWidth, 0, false).
		AddItem(continueButton, 0, 0, false).
		AddItem(abortButton, 0, 0, false).
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
	t.refList = refList
	t.mainPanels = mainPanels
	t.statusBar = statusBar
	t.statusRow = statusRow
	t.keybindHint = keybindHint
	t.continueButton = continueButton
	t.abortButton = abortButton

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
	t.commitButton = commitButton
	t.amendCheck = amendCheck

	t.tabStyle = defaultStyle
	t.tabActiveStyle = accentStyle
	t.selectedStyle = selectedStyle
	t.headerStyle = headerStyle
	t.headerSelectedStyle = headerSelectedStyle
	// 边框和标题在未聚焦/聚焦时分别使用这两种颜色。
	t.borderColor = colorBorder.cell
	t.borderFocusColor = colorAccent.cell

	t.activeTab = tabCommit
	t.statusBusy = make(map[string]string)
	t.syncFrame = -1

	// SetRoot 只会对 commitList 触发 focus 回调，其余需手动初始化为未聚焦
	t.setFocusBorder(filterText.Box, false)
	t.setFocusBorder(refList.Box, false)
	t.setFocusBorder(commitList.Box, true)
	t.setFocusBorder(commitInfo.Box, false)
	t.setFocusBorder(fileList.Box, false)
	t.setFocusBorder(fileDiff.Box, false)
	t.setFocusBorder(changeList.Box, false)
	t.setFocusBorder(changeDiff.Box, false)
	t.setFocusBorder(commitSubject.Box, false)
	t.setFocusBorder(commitDescription.Box, false)

	return root
}

// setupPrompt 给确认弹窗铺上弹窗底色，并作为独立页面打开。
// Modal 默认用蓝色 ContrastBackgroundColor，而且 Box、按钮、边框都需要单独覆盖。
func (t *TUI) setupPrompt(name string, prompt *tview.Modal) {
	prompt.SetBackgroundColor(colorPopup.cell)
	prompt.Box.SetBackgroundColor(colorPopup.cell)
	prompt.Box.SetBorderStyle(tcell.StyleDefault.
		Foreground(tview.Styles.BorderColor).
		Background(colorPopup.cell))
	prompt.SetButtonBackgroundColor(colorPopup.cell)
	prompt.SetButtonActivatedStyle(tcell.StyleDefault.
		Background(colorText.cell).
		Foreground(colorOnAccent.cell))

	// 只有弹窗自身接收鼠标；其余区域直接消费，避免点击穿透到下面的主界面。
	prompt.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if prompt.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	t.promptVisible = true
	t.openPopup(name, prompt, prompt)
}

func (t *TUI) setupTasks() {
	post := func(f func()) { t.app.QueueUpdateDraw(f) }

	// compareKey 返回 compare 在 Task 缓存 key 中的标识。
	compareKey := func(compare *core.Commit) string {
		if compare == nil {
			return ""
		}
		return compare.Hash
	}

	t.tasks.refs = async.New(async.Config[core.Refs, refsRequest]{
		Key: func(refsRequest) string { return "refs" },
		Run: func(ctx context.Context, _ refsRequest) (core.Refs, error) {
			return t.Refs(ctx)
		},
		Post:      post,
		OnRunning: t.showRefsRunning,
		OnResult:  t.showRefs,
		NoCache:   true,
	})

	t.tasks.commits = async.New(async.Config[[]core.Commit, commitsRequest]{
		Key: func(commitsRequest) string { return "commits" },
		Run: func(ctx context.Context, _ commitsRequest) ([]core.Commit, error) {
			return t.History(ctx)
		},
		Post:      post,
		OnRunning: t.showCommitsRunning,
		OnResult:  t.showCommits,
		NoCache:   true,
	})

	t.tasks.files = async.New(async.Config[[]core.FileChange, filesRequest]{
		Key: func(req filesRequest) string {
			return fmt.Sprintf("%s\x00%s", req.commit.Hash, compareKey(req.compare))
		},
		Run: func(ctx context.Context, req filesRequest) ([]core.FileChange, error) {
			return t.Files(ctx, req.commit, req.compare)
		},
		Post:      post,
		OnRunning: t.showFilesRunning,
		OnResult:  t.showFiles,
	})

	t.tasks.diff = async.New(async.Config[string, diffRequest]{
		Key: func(req diffRequest) string {
			return fmt.Sprintf("%s\x00%s\x00%s\x00%s", req.commit.Hash, compareKey(req.compare), req.change.Path, req.options)
		},
		Run: func(ctx context.Context, req diffRequest) (string, error) {
			return renderDiff(t.Diff(ctx, req.commit, req.compare, req.change, req.options))
		},
		Post:      post,
		OnRunning: t.showFileDiffRunning,
		OnResult:  t.showFileDiff,
	})

	t.tasks.localFiles = async.New(async.Config[[]core.FileChange, localFilesRequest]{
		Key: func(localFilesRequest) string { return "local" },
		Run: func(ctx context.Context, _ localFilesRequest) ([]core.FileChange, error) {
			return t.LocalFiles(ctx)
		},
		Post:      post,
		OnRunning: t.showLocalRunning,
		OnResult:  t.showLocal,
		NoCache:   true,
	})

	t.tasks.localDiff = async.New(async.Config[string, localDiffRequest]{
		Key: func(req localDiffRequest) string {
			return fmt.Sprintf("%d\x00%s\x00%s", req.change.Scope, req.change.Path, req.options)
		},
		Run: func(ctx context.Context, req localDiffRequest) (string, error) {
			return renderDiff(t.LocalDiff(ctx, req.change, req.options))
		},
		Post:      post,
		OnRunning: t.showLocalDiffRunning,
		OnResult:  t.showLocalDiff,
	})

	t.tasks.fileHistory = async.New(async.Config[[]core.Commit, historyRequest]{
		Key: func(req historyRequest) string { return req.path },
		Run: func(ctx context.Context, req historyRequest) ([]core.Commit, error) {
			return t.FileHistory(ctx, req.path)
		},
		Post:     post,
		OnResult: t.showHistory,
	})

	t.tasks.fileHistoryDiff = async.New(async.Config[string, historyDiffRequest]{
		Key: func(req historyDiffRequest) string {
			return fmt.Sprintf("%s\x00%s\x00%s", req.commit.Hash, req.change.Path, req.options)
		},
		Run: func(ctx context.Context, req historyDiffRequest) (string, error) {
			return renderDiff(t.Diff(ctx, req.commit, nil, req.change, req.options))
		},
		Post:     post,
		OnResult: t.showHistoryDiff,
	})

	t.tasks.checkout = async.New(async.Config[string, checkoutRequest]{
		Key: func(req checkoutRequest) string { return req.name() },
		Run: func(ctx context.Context, req checkoutRequest) (string, error) {
			if req.tag.Name != "" {
				return t.Checkout(ctx, req.tag)
			}
			return t.CheckoutBranch(ctx, req.branch)
		},
		Post:      post,
		OnRunning: t.showCheckoutRunning,
		OnResult:  t.showCheckout,
		NoCache:   true,
	})

	t.tasks.create = async.New(async.Config[string, createRequest]{
		Key: func(req createRequest) string {
			return fmt.Sprintf("%d\x00%s", req.op, req.name)
		},
		Run: func(ctx context.Context, req createRequest) (string, error) {
			switch req.op {
			case opCreateBranch:
				return t.CreateBranch(ctx, req.name, req.commit)
			case opCreateTag:
				return t.CreateTag(ctx, req.name, req.commit)
			default:
				return "", fmt.Errorf("unknown create operation")
			}
		},
		Post:      post,
		OnRunning: t.showCreateRunning,
		OnResult:  t.showCreate,
		NoCache:   true,
	})

	t.tasks.delete = async.New(async.Config[string, deleteRequest]{
		Key: func(req deleteRequest) string {
			return fmt.Sprintf("%t\x00%s", req.force, req.name())
		},
		Run: func(ctx context.Context, req deleteRequest) (string, error) {
			switch {
			case req.tag.Name != "":
				return req.name(), t.DeleteTag(ctx, req.tag.Name)
			case req.branch.Remote:
				return req.name(), t.DeleteRemoteBranch(ctx, req.branch)
			default:
				return req.name(), t.DeleteBranch(ctx, req.branch.Name, req.force)
			}
		},
		Post:      post,
		OnRunning: t.showDeleteRunning,
		OnResult:  t.showDelete,
		NoCache:   true,
	})

	t.tasks.sync = async.New(async.Config[string, syncOpRequest]{
		Key: func(req syncOpRequest) string { return fmt.Sprintf("%d", req.op) },
		Run: func(ctx context.Context, req syncOpRequest) (string, error) {
			switch req.op {
			case opPull:
				return t.Pull(ctx)
			case opPush:
				return t.Push(ctx, false)
			case opPushForce:
				return t.Push(ctx, true)
			default:
				return t.Fetch(ctx)
			}
		},
		Post:      post,
		OnRunning: t.showSyncRunning,
		OnResult:  t.showSync,
		NoCache:   true,
	})

	t.tasks.commit = async.New(async.Config[string, commitOpRequest]{
		Key: func(req commitOpRequest) string {
			return fmt.Sprintf("%d\x00%s\x00%d\x00%d", req.op, req.commit.Hash, req.resetMode, req.mergeMode)
		},
		Run: func(ctx context.Context, req commitOpRequest) (string, error) {
			switch req.op {
			case opReset:
				return t.Reset(ctx, req.commit, req.resetMode)
			case opRebase:
				return t.Rebase(ctx, req.commit)
			case opRebaseInteractive:
				return t.RebaseInteractive(ctx, req.commit, req.rebaseActions)
			case opReword:
				return t.Reword(ctx, req.commit, req.subject, req.description)
			case opRebaseContinue:
				return t.RebaseContinue(ctx)
			case opRebaseAbort:
				return t.RebaseAbort(ctx)
			case opMerge:
				return t.Merge(ctx, req.commit, req.mergeMode)
			case opMergeContinue:
				return t.MergeContinue(ctx)
			case opMergeAbort:
				return t.MergeAbort(ctx)
			case opSquashContinue:
				return t.SquashContinue(ctx)
			case opSquashAbort:
				return t.SquashAbort(ctx)
			case opCherryPick:
				return t.CherryPick(ctx, req.commit)
			case opCherryPickContinue:
				return t.CherryPickContinue(ctx)
			case opCherryPickAbort:
				return t.CherryPickAbort(ctx)
			default:
				return t.Commit(ctx, req.subject, req.description, req.amend)
			}
		},
		Post:      post,
		OnRunning: t.showCommitOpRunning,
		OnResult:  t.showCommitOp,
		NoCache:   true,
	})

	t.tasks.change = async.New(async.Config[string, changeOpRequest]{
		Key: func(req changeOpRequest) string {
			return fmt.Sprintf("%d\x00%s", req.op, req.path)
		},
		Run: func(ctx context.Context, req changeOpRequest) (string, error) {
			switch req.op {
			case opStage:
				return req.path, t.Stage(ctx, req.changes)
			case opUnstage:
				return req.path, t.Unstage(ctx, req.changes)
			default:
				if len(req.changes) == 0 {
					return req.path, nil
				}
				return req.path, t.Discard(ctx, req.changes[0])
			}
		},
		Post:      post,
		OnRunning: t.showChangeOpRunning,
		OnResult:  t.showChangeOp,
		NoCache:   true,
	})
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
// onClick 在默认的点击处理之前调用，可为 nil，用来区分“点击移动光标”和“再次点击当前节点”。
func (t *TUI) focusOnScrollTree(p scrollFocusable, onClick func()) {
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
		case tview.MouseLeftClick:
			if onClick != nil {
				onClick()
			}
		}
		return action, event
	})
}

// popupVisible 表示有弹窗正在显示；用于屏蔽底层快捷键和防止弹窗叠加。
func (t *TUI) popupVisible() bool {
	return t.promptVisible || t.rebaseVisible || t.rewordVisible || t.historyVisible || t.commitInfoVisible || t.helpVisible
}

/** EVENT */

// quit 先停止 Task，再等后台 goroutine 退出，最后停止事件循环。
// 不能在事件循环里同步等待，否则 Post 无法被消费。
func (t *TUI) quit() {
	if t.quitting {
		return
	}
	t.quitting = true

	// 先停掉动画，避免 ticker 在 app.Stop 之后继续投递更新。
	t.stopSyncSpinner()

	// Close 是非阻塞的，先阻止新的请求并取消在途 Run。
	t.tasks.close()

	go func() {
		t.tasks.wait()
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

// focusChain 返回当前 Tab/Backtab 的循环顺序；弹窗显示时只在弹窗内部切换。
func (t *TUI) focusChain() []tview.Primitive {
	if t.rebaseVisible {
		return t.rebaseForm.focusables()
	}
	if t.rewordVisible {
		return t.rewordForm.focusables()
	}
	if t.historyVisible {
		return t.historyView.focusables()
	}
	if t.activeTab == tabChange {
		return []tview.Primitive{t.changeTab, t.commitTab, t.filterText, t.refList, t.changeList, t.changeDiff, t.commitSubject, t.commitDescription, t.commitButton}
	}
	return []tview.Primitive{t.changeTab, t.commitTab, t.filterText, t.refList, t.commitList, t.commitInfo, t.fileList, t.fileDiff}
}

// setFocusBorder 按焦点状态切换边框和标题颜色。
func (t *TUI) setFocusBorder(box *tview.Box, focused bool) {
	color := t.borderColor
	if focused {
		color = t.borderFocusColor
	}
	box.SetBorderColor(color).SetTitleColor(color)
}

// openPopup 记录打开前的焦点，并把弹窗页面覆盖到主界面上。
// page 是添加到 Pages 的容器，focus 是实际接收键盘的控件；两者相同时传同一个值。
func (t *TUI) openPopup(name string, page, focus tview.Primitive) {
	if t.returnFocus == nil {
		t.returnFocus = t.app.GetFocus()
	}
	t.root.AddPage(name, page, true, true)
	t.app.SetFocus(focus)
}

// closePopup 关闭弹窗页面，并把焦点还给打开前的控件。
// 打开时焦点为空时，回到当前 tab 的列表。
func (t *TUI) closePopup(name string) {
	t.root.RemovePage(name)
	if t.returnFocus != nil {
		t.app.SetFocus(t.returnFocus)
		t.returnFocus = nil
		return
	}
	if t.activeTab == tabChange {
		t.app.SetFocus(t.changeList)
	} else {
		t.app.SetFocus(t.commitList)
	}
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
	// 焦点在左侧引用列表时 hiddenFocused 为 false，保持不动。
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

	// 数据可能命中 Task 缓存而不会触发 changed 回调，
	// 这里主动按当前 tab 和最新 options 重渲染 diff。
	t.refreshActiveDiff()
}

func (t *TUI) refreshTabStyles() {
	t.setTabStyle(t.changeTab, t.activeTab == tabChange)
	t.setTabStyle(t.commitTab, t.activeTab == tabCommit)
}

func (t *TUI) setTabStyle(button *tview.Button, active bool) {
	if active {
		// 激活 tab 保持强调色，聚焦时加粗，提示 Tab 焦点停在这里。
		button.SetStyle(t.tabActiveStyle).SetActivatedStyle(t.tabActiveStyle.Bold(true))
		return
	}
	// 未激活 tab 获得焦点时用选中底色，和列表的选中态一致。
	button.SetStyle(t.tabStyle).SetActivatedStyle(t.selectedStyle)
}

// refreshRefs 按当前 Branches / Tags 重建引用树；selected 为要选中的引用名，
// 为空或找不到时默认选中第一个引用。
func (t *TUI) refreshRefs(selected string) {
	if t.refList == nil {
		return
	}

	filter := ""
	if t.filterText != nil {
		filter = strings.ToLower(strings.TrimSpace(t.filterText.GetText()))
	}

	refRoot := buildRefTree(t.Branches, t.Tags, filter, t.HeadRef, t.syncFrame, t.refFolderExpanded, t.headerStyle, t.headerSelectedStyle, t.selectedStyle)
	t.refList.SetRoot(refRoot)

	if node := t.refNode(selected); node != nil {
		t.refList.SetCurrentNode(node)
		return
	}
	// 默认选中第一个引用，而不是分组标题或目录。
	if node := firstRefNode(refRoot); node != nil {
		t.refList.SetCurrentNode(node)
	}
}

// toggleRefFolder 切换引用树目录的展开状态，并记住用户的选择以便重建引用树时恢复。
func (t *TUI) toggleRefFolder(node *tview.TreeNode) {
	folder, ok := node.GetReference().(folder)
	if !ok {
		return
	}
	expanded := !node.IsExpanded()
	setFolderExpanded(node, expanded)
	if t.refFolderExpanded == nil {
		t.refFolderExpanded = make(map[string]bool)
	}
	t.refFolderExpanded[folder.key] = expanded
}

// refreshCommit 刷新选中 commit 的信息，并异步请求文件列表。
// force 为 true 时无论选中项是否变化都重绘 commitInfo。
func (t *TUI) refreshCommit(force bool) {
	row := t.commitList.CurrentItem()
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

// compareCommit 由 Ctrl+点击触发：把 row 对应的 commit 设为对比基准。
func (t *TUI) compareCommit(row int) {
	if t.commitList == nil || row < 0 || row >= len(t.Commits) {
		return
	}
	compare := t.Commits[row]

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

// jumpToParent 把选中项移动到当前 commit 的第一个 parent。
func (t *TUI) jumpToParent() {
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	parents := t.Commits[row].Parents
	if len(parents) == 0 {
		return
	}
	if parent := t.commitRow(parents[0]); parent >= 0 {
		t.commitList.SetCurrentItem(parent)
	}
}

// jumpToChild 把选中项移动到列表上方最近的第一个 child（当前 commit 是其 first parent）。
func (t *TUI) jumpToChild() {
	row := t.commitList.CurrentItem()
	if row <= 0 || row >= len(t.Commits) {
		return
	}
	hash := t.Commits[row].Hash
	// topo-order 下 child 都在当前行上方，从近到远查找；只认 first-parent 以和 jumpToParent 对偶。
	for i := row - 1; i >= 0; i-- {
		if parents := t.Commits[i].Parents; len(parents) > 0 && parents[0] == hash {
			t.commitList.SetCurrentItem(i)
			return
		}
	}
}

// resetCommit 先弹窗选择 reset 模式，确认后把当前分支重置到选中的 commit。
func (t *TUI) resetCommit() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	message := fmt.Sprintf("Reset %s to %s?\n\n%s\n\n",
		tview.Escape(t.HeadRef), tview.Escape(commit.ShortHash()), tview.Escape(commit.Subject))

	message += colorMuted.markup("Soft") + " keeps index and working tree\n"
	message += colorMuted.markup("Mixed") + " resets index, keeps working tree\n"
	message += colorDanger.markup("Hard") + " discards index and working tree changes"

	const page = "reset"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Soft", "Mixed", "Hard"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			switch buttonIndex {
			case 1:
				t.executeReset(commit, core.ResetSoft)
			case 2:
				t.executeReset(commit, core.ResetMixed)
			case 3:
				t.executeReset(commit, core.ResetHard)
			}
		})

	t.setupPrompt(page, prompt)
}

// rebaseCommit 先弹窗确认，再把当前分支 rebase 到选中的 commit。
func (t *TUI) rebaseCommit() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	base := t.Commits[row]

	message := fmt.Sprintf("Rebase %s onto %s?\n\n%s\n\n",
		tview.Escape(t.HeadRef), tview.Escape(base.ShortHash()), tview.Escape(base.Subject))

	message += colorMuted.markup("Local changes will be stashed and restored automatically.")

	const page = "rebase"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Rebase"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeRebase(base)
			}
		})

	t.setupPrompt(page, prompt)
}

// rebaseCommitInteractive 先弹窗选择每个 commit 的动作，确认后执行 interactive rebase。
func (t *TUI) rebaseCommitInteractive() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.rebaseVisible || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	base := t.Commits[row]

	commits := rebaseCommits(t.Commits, t.HeadHash, base.Hash)
	if len(commits) == 0 {
		t.statusDone("commit", "no commits to rebase")
		return
	}

	popup := &rebasePopup{
		base:    base,
		commits: commits,
		actions: make([]core.RebaseAction, len(commits)),
	}
	for i := range popup.actions {
		popup.actions[i] = core.RebasePick
	}

	message := fmt.Sprintf("Rebase %s onto %s?\n\n%s",
		tview.Escape(t.HeadRef), tview.Escape(base.ShortHash()), tview.Escape(base.Subject))

	rebase := newRebaseForm(
		message,
		renderGraph(commits, core.ComputeGraph(commits), false),
		popup.actions,
		t.selectedStyle,
		t.borderColor,
		t.borderFocusColor,
		t.cycleRebaseAction,
		t.closeRebaseInteractive,
		t.confirmRebaseInteractive,
	)

	t.rebasePopup = popup
	t.rebaseForm = rebase
	t.rebaseVisible = true
	t.root.AddPage("interactive-rebase", rebase.view, true, true)
	t.app.SetFocus(rebase.list)
}

// cycleRebaseAction 切换当前选中 commit 的 rebase 动作并刷新列表。
func (t *TUI) cycleRebaseAction(delta int) {
	if !t.rebaseVisible {
		return
	}
	t.rebasePopup.cycleAction(t.rebaseForm.list.CurrentItem(), delta)
	t.rebaseForm.SetActions(t.rebasePopup.actions)
	t.rebaseForm.ResetHint()
}

// closeRebaseInteractive 关闭 interactive rebase 弹窗，把焦点还给 commit 列表。
func (t *TUI) closeRebaseInteractive() {
	if !t.rebaseVisible {
		return
	}
	t.rebasePopup = nil
	t.rebaseForm = nil
	t.rebaseVisible = false
	t.closePopup("interactive-rebase")
}

// confirmRebaseInteractive 校验动作后关闭弹窗并执行 interactive rebase。
func (t *TUI) confirmRebaseInteractive() {
	if !t.rebaseVisible {
		return
	}
	popup, form := t.rebasePopup, t.rebaseForm
	actions, err := popup.validateActions()
	if err != nil {
		form.SetErrorHint(err.Error())
		return
	}
	base := popup.base
	t.closeRebaseInteractive()
	t.executeRebaseInteractive(base, actions)
}

// rewordCommit 先弹窗编辑提交信息，确认后改写选中的 commit。
func (t *TUI) rewordCommit() {
	if t.tasks.commit == nil || t.root == nil || t.popupVisible() || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	popup := &rewordPopup{commit: commit}

	if message := rewordValidation(t.Commits, t.HeadHash, commit); message != "" {
		t.statusDone("commit", message)
		return
	}

	reword := newRewordForm(
		commit,
		t.selectedStyle,
		t.borderColor,
		t.borderFocusColor,
		t.closeReword,
		t.confirmReword,
	)
	t.rewordPopup = popup
	t.rewordForm = reword
	t.rewordVisible = true
	t.root.AddPage("reword", reword.view, true, true)
	t.app.SetFocus(reword.subject)
}

// closeReword 关闭 reword 弹窗，把焦点还给 commit 列表。
func (t *TUI) closeReword() {
	if !t.rewordVisible {
		return
	}
	t.rewordPopup = nil
	t.rewordForm = nil
	t.rewordVisible = false
	t.closePopup("reword")
}

// confirmReword 读取表单内容，关闭弹窗并改写提交信息。
func (t *TUI) confirmReword() {
	if !t.rewordVisible {
		return
	}
	subject := strings.TrimSpace(t.rewordForm.subject.GetText())
	if subject == "" {
		t.rewordForm.SetErrorHint("commit subject is required")
		return
	}
	description := strings.TrimSpace(t.rewordForm.description.GetText())
	commit := t.rewordPopup.commit
	t.closeReword()
	t.executeReword(commit, subject, description)
}

// continueRebase 在冲突解决并暂存后继续 rebase。
func (t *TUI) continueRebase() {
	if t.tasks.commit == nil || t.tasks.commit.Running() {
		return
	}
	t.executeCommitOp(opRebaseContinue)
}

// abortRebase 先弹窗确认，再取消未完成的 rebase。
func (t *TUI) abortRebase() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}

	const page = "rebase-abort"

	prompt := tview.NewModal().
		SetText("Abort rebase?\n\nAll conflict resolutions will be lost.").
		AddButtons([]string{"Cancel", "Abort"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeCommitOp(opRebaseAbort)
			}
		})

	t.setupPrompt(page, prompt)
}

// mergeCommit 先弹窗确认，再把选中的 commit merge 到当前分支。
func (t *TUI) mergeCommit() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	message := fmt.Sprintf("Merge %s into %s?\n\n%s\n\n",
		tview.Escape(commit.ShortHash()), tview.Escape(t.HeadRef), tview.Escape(commit.Subject))

	message += colorMuted.markup("Merge") + " fast-forwards when possible, otherwise creates a merge commit\n"
	message += colorMuted.markup("Squash") + " combines changes into one new commit\n"
	message += colorMuted.markup("No FF") + " always creates a merge commit\n\n"
	message += colorMuted.markup("Local changes will be stashed and restored automatically.")

	const page = "merge"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Merge", "Squash", "No FF"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			switch buttonIndex {
			case 1:
				t.executeMerge(commit, core.MergeFF)
			case 2:
				t.executeMerge(commit, core.MergeSquash)
			case 3:
				t.executeMerge(commit, core.MergeNoFF)
			}
		})

	t.setupPrompt(page, prompt)
}

// continueMerge 在冲突解决并暂存后继续 merge。
func (t *TUI) continueMerge() {
	if t.tasks.commit == nil || t.tasks.commit.Running() {
		return
	}
	t.executeCommitOp(opMergeContinue)
}

// abortMerge 先弹窗确认，再取消未完成的 merge。
func (t *TUI) abortMerge() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}

	const page = "merge-abort"

	prompt := tview.NewModal().
		SetText("Abort merge?\n\nAll conflict resolutions will be lost.").
		AddButtons([]string{"Cancel", "Abort"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeCommitOp(opMergeAbort)
			}
		})

	t.setupPrompt(page, prompt)
}

// continueSquash 提交待完成的 squash（冲突解决或自动提交失败后）；冲突未解决时 git 会报错。
func (t *TUI) continueSquash() {
	if t.tasks.commit == nil || t.tasks.commit.Running() {
		return
	}
	t.executeCommitOp(opSquashContinue)
}

// abortSquash 先弹窗确认，再取消待完成的 squash。
func (t *TUI) abortSquash() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}

	const page = "squash-abort"

	prompt := tview.NewModal().
		SetText("Abort squash?\n\nStaged squash changes and conflict resolutions will be lost.\n\n" + colorMuted.markup("Local changes stashed before the merge will be restored.")).
		AddButtons([]string{"Cancel", "Abort"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeCommitOp(opSquashAbort)
			}
		})

	t.setupPrompt(page, prompt)
}

// cherryPickCommit 先弹窗确认，再把选中的 commit cherry-pick 到当前分支。
func (t *TUI) cherryPickCommit() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]

	message := fmt.Sprintf("Cherry-pick %s onto %s?\n\n%s\n\n",
		tview.Escape(commit.ShortHash()), tview.Escape(t.HeadRef), tview.Escape(commit.Subject))

	message += colorMuted.markup("Local changes will be stashed and restored automatically.")

	const page = "cherry-pick"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Cherry-pick"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeCherryPick(commit)
			}
		})

	t.setupPrompt(page, prompt)
}

// continueCherryPick 在冲突解决并暂存后继续 cherry-pick。
func (t *TUI) continueCherryPick() {
	if t.tasks.commit == nil || t.tasks.commit.Running() {
		return
	}
	t.executeCommitOp(opCherryPickContinue)
}

// abortCherryPick 先弹窗确认，再取消未完成的 cherry-pick。
func (t *TUI) abortCherryPick() {
	if t.tasks.commit == nil || t.root == nil || t.promptVisible || t.tasks.commit.Running() {
		return
	}

	const page = "cherry-pick-abort"

	prompt := tview.NewModal().
		SetText("Abort cherry-pick?\n\nAll conflict resolutions will be lost.\n\n" + colorMuted.markup("Local changes stashed before the cherry-pick will be restored.")).
		AddButtons([]string{"Cancel", "Abort"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeCommitOp(opCherryPickAbort)
			}
		})

	t.setupPrompt(page, prompt)
}

// commitChanges 用表单内容提交暂存区改动；Amend 勾选时改写 HEAD。
func (t *TUI) commitChanges() {
	if t.tasks.commit == nil || t.tasks.commit.Running() {
		return
	}
	subject := strings.TrimSpace(t.commitSubject.GetText())
	if subject == "" {
		t.statusDone("commit", "commit subject is required")
		return
	}
	description := strings.TrimSpace(t.commitDescription.GetText())
	t.executeCommit(subject, description, t.amendCheck.IsChecked())
}

// openCommitInfo 弹窗显示当前 commit 的完整信息；j/k、PgUp/PgDn 可滚动。
func (t *TUI) openCommitInfo() {
	if t.root == nil || t.promptVisible || t.historyVisible || t.commitInfoVisible {
		return
	}
	commit := t.CurrentCommit
	if commit.Hash == "" {
		return
	}

	commitInfo := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetText(renderCommitFull(commit))

	commitInfo.SetTitle(" commit ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { t.setFocusBorder(commitInfo.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(commitInfo.Box, false) })

	// TextView 会用自己的背景色重刷内容区，边框也要单独设底色。
	commitInfo.SetBackgroundColor(colorPopup.cell)
	commitInfo.SetBorderStyle(tcell.StyleDefault.
		Foreground(t.borderFocusColor).
		Background(colorPopup.cell))

	// 弹窗铺满 mainPage 所在区域，左右各留 15 列、上下各留 3 行，
	// 和 history 弹窗保持一致的留白。
	inner := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 15, 0, false).
		AddItem(commitInfo, 0, 1, true).
		AddItem(nil, 15, 0, false)

	popup := tview.NewFlex().
		AddItem(nil, 35, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 35, 0, false)

	// 只有信息面板接收鼠标；其余区域直接消费，避免点击穿透到下面的主界面。
	popup.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if commitInfo.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	t.commitInfoVisible = true
	t.openPopup("commit-info", popup, commitInfo)
}

// closeCommitInfo 关闭 commit 详情弹窗。
func (t *TUI) closeCommitInfo() {
	if !t.commitInfoVisible {
		return
	}
	t.commitInfoVisible = false
	t.closePopup("commit-info")
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

func (t *TUI) selectFile(node *tview.TreeNode) {
	// 文件列表加载中时，旧树节点属于旧 commit，不能拿来请求新 commit 的 diff。
	if t.tasks.files.Running() {
		return
	}
	t.requestFileDiff(t.CurrentCommit, t.CompareCommit, node)
}

func (t *TUI) selectLocalFile(node *tview.TreeNode) {
	// 列表加载中时，旧树节点属于旧数据，不能拿来发起新的 diff 请求。
	if t.tasks.localFiles.Running() {
		return
	}
	t.requestLocalDiff(node)
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

	view := newHistoryView(
		change.Path,
		t.selectedStyle,
		t.borderColor,
		t.borderFocusColor,
		t.selectHistory,
		t.locateHistory,
		t.syncHistoryDiffWidth,
		t.toggleWhitespace,
		t.toggleSideBySide,
	)
	t.focusOnScroll(view.list)
	t.focusOnScroll(view.diff)

	t.historyPopup = &historyPopup{change: change}
	t.historyView = view
	t.historyVisible = true
	t.openPopup("history", view.view, view.list)

	t.requestHistory(change.Path)
}

// closeHistory 关闭文件历史弹窗。
func (t *TUI) closeHistory() {
	if !t.historyVisible {
		return
	}
	t.historyVisible = false
	t.tasks.fileHistory.Cancel()
	t.tasks.fileHistoryDiff.Cancel()
	t.historyPopup = nil
	t.historyView = nil
	t.closePopup("history")
}

// selectHistory 在历史列表中切换选中项时请求对应 commit 的文件 diff。
func (t *TUI) selectHistory() {
	if !t.historyVisible {
		return
	}
	commit, ok := t.historyPopup.commitAt(t.historyView.list.CurrentItem())
	if !ok {
		return
	}
	t.requestHistoryDiff(commit)
}

// locateHistory 关闭历史弹窗，切到 commit tab 并定位到选中的 commit。
func (t *TUI) locateHistory() {
	if !t.historyVisible {
		return
	}
	commit, ok := t.historyPopup.commitAt(t.historyView.list.CurrentItem())
	if !ok {
		return
	}
	hash := commit.Hash

	t.closeHistory()
	t.pendingCommit = hash
	if t.activeTab != tabCommit {
		t.switchTab(tabCommit)
	} else if row := t.commitRow(hash); row >= 0 {
		t.pendingCommit = ""
		t.commitList.SetCurrentItem(row)
	} else {
		t.reloadCommits()
	}
	t.app.SetFocus(t.commitList)
}

// toggleLocalChange 用空格暂存/取消暂存当前文件；目录节点处理目录内的全部变更。
func (t *TUI) toggleLocalChange() {
	if t.tasks.change == nil || t.tasks.localFiles.Running() || t.tasks.change.Running() {
		return
	}
	node := t.changeList.GetCurrentNode()
	if node == nil {
		return
	}

	if change, ok := node.GetReference().(core.FileChange); ok {
		op := opStage
		if change.Scope == core.ScopeStaged {
			op = opUnstage
		}
		t.executeChangeOp(op, []core.FileChange{change}, change.Path)
		return
	}

	if _, isFolder := node.GetReference().(folder); !isFolder {
		return
	}
	changes := folderChanges(node)
	if len(changes) == 0 {
		// 没有可操作的文件时保留展开/收起行为。
		setFolderExpanded(node, !node.IsExpanded())
		return
	}

	// 同一组内的文件 scope 一致，用第一项决定方向；目录路径用于状态栏文案。
	op := opStage
	if changes[0].Scope == core.ScopeStaged {
		op = opUnstage
	}
	t.executeChangeOp(op, changes, folderPath(t.changeList.GetPath(node)))
}

// discardLocalChange 先弹窗确认，再丢弃当前文件的改动。
func (t *TUI) discardLocalChange() {
	if t.tasks.change == nil || t.root == nil || t.promptVisible || t.tasks.localFiles.Running() || t.tasks.change.Running() {
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

	const page = "discard"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Discard"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeChangeOp(opDiscard, []core.FileChange{change}, change.Path)
			}
		})

	t.setupPrompt(page, prompt)
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
		if t.changeList == nil || (t.tasks.localFiles != nil && t.tasks.localFiles.Running()) {
			return
		}
		if node := t.changeList.GetCurrentNode(); node != nil {
			if _, ok := node.GetReference().(core.FileChange); ok {
				t.requestLocalDiff(node)
			}
		}
		return
	}

	if t.fileList == nil || (t.tasks.files != nil && t.tasks.files.Running()) {
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
	commit, ok := t.historyPopup.commitAt(t.historyView.list.CurrentItem())
	if ok {
		t.requestHistoryDiff(commit)
	}
}

// checkoutRef 对选中的分支或标签执行 checkout：
// 本地分支直接切换；远端分支切换到同名本地分支，不存在时创建跟踪分支；标签进入 detached HEAD。
func (t *TUI) checkoutRef(node *tview.TreeNode) {
	if t.tasks.checkout == nil || t.tasks.checkout.Running() {
		return
	}
	t.executeCheckout(node)
}

// syncBranch 拉取当前分支、推送当前分支或拉取远端引用；同一时间只允许一个同步操作。
func (t *TUI) syncBranch(op syncOp) {
	if t.tasks.sync == nil || t.tasks.sync.Running() {
		return
	}
	t.executeSync(op)
}

// createRefFromCommit 从选中的 commit 创建引用。
func (t *TUI) createRefFromCommit(op createOp) {
	row := t.commitList.CurrentItem()
	if row < 0 || row >= len(t.Commits) {
		return
	}
	commit := t.Commits[row]
	t.createRef(op, &commit)
}

// createRef 弹出输入框，创建分支或标签。
// commit 为 nil 时从 HEAD 创建，否则从该 commit 创建。
func (t *TUI) createRef(op createOp, commit *core.Commit) {
	if t.tasks.create == nil || t.root == nil || t.promptVisible || t.tasks.create.Running() {
		return
	}

	label := "Branch name "
	action := "Create and checkout a new branch"
	if op == opCreateTag {
		label = "Tag name "
		action = "Create a new tag"
	}

	message := action + " from HEAD"
	if commit != nil {
		message = action + " from " + commit.ShortHash()
		if commit.Subject != "" {
			message += "\n\n" + commit.Subject
		}
	}

	const page = "create-ref"

	prompt := newModalForm(62).
		SetText(message).
		SetLabel(label).
		SetButtons([]string{"Cancel", "Create"})

	prompt.SetDoneFunc(func(buttonIndex int, _ string) {
		name := ""
		if buttonIndex == 1 {
			name = strings.TrimSpace(prompt.GetInputText())
			if name == "" {
				return
			}
		}

		t.promptVisible = false
		t.closePopup(page)

		if buttonIndex == 1 {
			t.executeCreate(op, name, commit)
		}
	})

	t.promptVisible = true
	t.openPopup(page, prompt, prompt)
}

// deleteRef 先弹窗确认，再删除选中的本地分支、远端分支或标签。
func (t *TUI) deleteRef(node *tview.TreeNode) {
	if t.tasks.delete == nil || t.root == nil || t.promptVisible || t.tasks.delete.Running() {
		return
	}

	var req deleteRequest
	switch ref := node.GetReference().(type) {
	case core.Branch:
		req.branch = ref
	case core.Tag:
		req.tag = ref
	default:
		return
	}

	buttons := []string{"Cancel", "Delete"}
	var message string
	switch {
	case req.tag.Name != "":
		message = fmt.Sprintf("Delete tag %s?\n\n%s", tview.Escape(req.tag.Name),
			colorDanger.markup("This cannot be undone."))
	case req.branch.Remote:
		message = fmt.Sprintf("Delete remote branch %s on %s?\n\n%s",
			tview.Escape(req.branch.LocalName()), tview.Escape(req.branch.RemoteName),
			colorDanger.markup("This cannot be undone."))
	default:
		buttons = []string{"Cancel", "Delete", "Force"}
		message = fmt.Sprintf("Delete branch %s?\n\n%s\n\n", tview.Escape(req.branch.Name),
			colorDanger.markup("This cannot be undone."))
		message += colorMuted.markup("Force") + " deletes the branch even if it has unmerged commits"
	}

	const page = "delete-ref"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons(buttons).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			switch buttonIndex {
			case 1:
				t.executeDelete(req)
			case 2:
				req.force = true
				t.executeDelete(req)
			}
		})

	t.setupPrompt(page, prompt)
}

// openHelp 弹出快捷键帮助窗口。
func (t *TUI) openHelp() {
	if t.root == nil || t.popupVisible() {
		return
	}

	help := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetText(renderKeyBindings())
	help.SetBackgroundColor(colorPopup.cell)

	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(colorMuted.markup("j/k or ↑/↓ scroll · esc/q close"))
	hint.SetBackgroundColor(colorPopup.cell)

	// 提示固定在顶部，只有下面的快捷键列表随内容滚动；提示上下各留一行空行。
	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 1, 0, false).
		AddItem(hint, 1, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(help, 0, 1, true)

	// Flex 默认不清理自身区域，nil 占位行会露出下面的主界面；
	// 换成普通 Box 后先铺满弹窗底色再画子项。
	body.Box = tview.NewBox()

	body.SetTitle(" keybindings ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetBackgroundColor(colorPopup.cell)
	body.SetBorderStyle(tcell.StyleDefault.
		Foreground(t.borderFocusColor).
		Background(colorPopup.cell))

	// 焦点在下面滚动的列表上，边框跟着它切换高亮。
	help.SetFocusFunc(func() { t.setFocusBorder(body.Box, true) }).
		SetBlurFunc(func() { t.setFocusBorder(body.Box, false) })

	// 左右按比例留白，窄终端下也能保留足够的说明宽度。
	inner := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 10, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(nil, 10, 0, false)

	popup := tview.NewFlex().
		AddItem(nil, 0, 2, false).
		AddItem(inner, 0, 3, true).
		AddItem(nil, 0, 2, false)

	// 只有滚动列表接收鼠标；其余区域直接消费，避免点击穿透到下面的主界面。
	popup.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if help.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	t.helpVisible = true
	t.openPopup("help", popup, help)
}

// closeHelp 关闭快捷键帮助弹窗。
func (t *TUI) closeHelp() {
	if !t.helpVisible {
		return
	}
	t.helpVisible = false
	t.closePopup("help")
}

/** REFS */

// reloadRefs 重新读取仓库级 refs（分支、标签和 HEAD）。
func (t *TUI) reloadRefs() {
	t.tasks.refs.Execute(refsRequest{})
}

func (t *TUI) showRefsRunning(_ refsRequest) {
	t.statusStart("refs", "loading refs")
}

func (t *TUI) showRefs(_ refsRequest, refs core.Refs, err error) {
	selected := t.pendingRef
	t.pendingRef = ""
	if selected == "" {
		selected = t.refName()
	}

	if err != nil {
		t.statusDone("refs", "refs: "+err.Error())
		return
	}
	t.statusDone("refs", "")

	t.Branches = refs.Branches
	t.Tags = refs.Tags
	t.HeadRef = refs.HeadRef
	t.HeadHash = refs.HeadHash
	t.Rebasing = refs.Rebasing
	t.Merging = refs.Merging
	t.Squashing = refs.Squashing
	t.CherryPicking = refs.CherryPicking
	t.renderInProgressActions()
	if t.repoHead != nil {
		t.repoHead.SetText(t.HeadRef)
	}
	t.refreshRefs(selected)

	// pull/checkout 后 HEAD 已变化，用 refs 里的新 HeadHash 定位到 HEAD 提交。
	// commits 刷新已由 refreshTab 或调用方发起，这里只应用选择。
	if t.pendingHead {
		t.pendingHead = false
		if row := t.commitRow(t.HeadHash); row >= 0 {
			t.commitList.SetCurrentItem(row)
		} else {
			t.pendingCommit = t.HeadHash
		}
	}
}

// refNode 返回引用 name 对应的节点，分支和标签都会匹配；name 为空或不存在时返回 nil。
func (t *TUI) refNode(name string) *tview.TreeNode {
	if t.refList == nil {
		return nil
	}
	return findRefNode(t.refList.GetRoot(), name)
}

// refName 返回引用列表当前选中节点的名字；分组标题和目录返回空。
func (t *TUI) refName() string {
	if t.refList == nil {
		return ""
	}
	node := t.refList.GetCurrentNode()
	if node == nil {
		return ""
	}
	return refName(node)
}

/** COMMIT */

// reloadCommits 重新读取 git log；加载期间保留旧列表，避免切 tab 时闪空。
func (t *TUI) reloadCommits() {
	t.tasks.commits.Execute(commitsRequest{})
}

func (t *TUI) showCommitsRunning(_ commitsRequest) {
	t.statusStart("commits", "loading commits")
}

func (t *TUI) showCommits(_ commitsRequest, commits []core.Commit, err error) {
	if err != nil {
		// 保留旧列表，只提示失败原因；下次切 tab 会重试。
		t.statusDone("commits", "commits: "+err.Error())
		return
	}
	t.statusDone("commits", "")

	// 按 hash 恢复选中项：topo-order 下刷新可能重排已有提交，不能依赖行号不变。
	row := t.commitList.CurrentItem()
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
	t.commitList.SetLines(renderGraph(commits, core.ComputeGraph(commits), true))
	// 刷新后 compare 可能已不存在（rebase/切分支），按 hash 重新确认。
	t.syncCompare()

	if len(commits) == 0 {
		t.CurrentCommit = core.Commit{}
		t.tasks.files.Cancel()
		t.statusDone("files", "")
		t.commitInfo.SetText("")
		t.fileList.SetTitle(" commit files ")
		t.fileList.SetRoot(tview.NewTreeNode(""))
		t.clearFileDiff("no commits")
		return
	}

	if found := t.commitRow(target); found >= 0 {
		row = found
	} else if found := t.commitRow(previous); found >= 0 {
		row = found
	}
	// SetCurrentItem 会像用户选中一样触发 refreshCommit，这里不再额外请求。
	t.commitList.SetCurrentItem(row)
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
		t.commitList.SetMarkedItem(row)
	}
}

// commitRow 返回 hash 在提交列表中的行号；hash 为空或不存在时返回 -1。
// 刷新 commits 后行号会整体移动，引用跳转必须按当前列表重新查询。
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

// requestFiles 请求 commit 相对 compare 的文件列表；相同 commit+compare 的重复请求由 Task 去重。
func (t *TUI) requestFiles(commit core.Commit, compare *core.Commit) {
	t.tasks.files.Execute(filesRequest{commit: commit, compare: compare})
}

// showFilesRunning 保留旧文件树，只通过 statusBar 提示加载。
func (t *TUI) showFilesRunning(_ filesRequest) {
	// 保留旧 diff 内容，但停掉在途请求，避免旧结果在文件加载期间回写面板。
	t.tasks.diff.Cancel()
	t.statusDone("diff", "")

	t.statusStart("files", "loading commit files")
}

func (t *TUI) showFiles(_ filesRequest, changes []core.FileChange, err error) {
	title := " commit files "
	var fileRoot *tview.TreeNode
	switch {
	case err != nil:
		fileRoot = tview.NewTreeNode("")
		fileRoot.AddChild(tview.NewTreeNode(colorDanger.markup("error") + " " + tview.Escape(err.Error())))
	case len(changes) == 0:
		title = " commit files (0) "
		fileRoot = tview.NewTreeNode("")
		fileRoot.AddChild(tview.NewTreeNode(colorMuted.markup("(no changes)")))
	default:
		title = fmt.Sprintf(" commit files (%d) ", len(changes))
		fileRoot = buildFileTree(changes, t.selectedStyle)
	}
	t.fileList.SetTitle(title)
	t.fileList.SetRoot(fileRoot)

	switch {
	case err != nil:
		// 失败由 Task 记录，下次请求同一 commit 时会重试。
		t.clearFileDiff("failed to load commit files")
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
		if _, isFolder := node.GetReference().(folder); isFolder {
			t.clearFileDiff("select a file")
		}
		return
	}
	t.tasks.diff.Execute(diffRequest{
		commit:  commit,
		change:  change,
		compare: compare,
		options: t.diffOptions(t.fileDiff),
	})
}

// showFileDiffRunning 保留旧 diff，只通过 statusBar 提示加载。
func (t *TUI) showFileDiffRunning(req diffRequest) {
	t.statusStart("diff", "loading diff "+req.change.Path)
}

func (t *TUI) showFileDiff(req diffRequest, text string, err error) {
	message := "diff: " + req.change.Path
	switch {
	case err != nil:
		text = colorDanger.markup("error") + " " + tview.Escape(err.Error())
		message = "diff failed: " + req.change.Path
	case text == "":
		text = colorMuted.markup("(no diff)")
		message = "no diff: " + req.change.Path
	}
	t.setDiff(t.fileDiff, "diff", req.change, text, message)
}

// clearFileDiff 清空 diff 并让在途请求失效；Task 会取消对应的 git 进程。
func (t *TUI) clearFileDiff(message string) {
	t.tasks.diff.Cancel()
	t.clearDiff(t.fileDiff, "diff", message)
}

/** LOCAL */

// reloadLocal 重新读取本地变更；加载期间保留旧列表，避免切 tab 时闪空。
func (t *TUI) reloadLocal() {
	t.tasks.localFiles.Execute(localFilesRequest{})
}

// showLocalRunning 保留旧变更树，只通过 statusBar 提示加载。
func (t *TUI) showLocalRunning(_ localFilesRequest) {
	// 保留旧 diff 内容，但停掉在途请求并丢弃缓存：
	// 文件内容可能已经变化，不能再用 scope+path 命中旧结果。
	t.tasks.localDiff.Reset()
	t.statusDone("local-diff", "")

	t.statusStart("local", "loading local changes")
}

func (t *TUI) showLocal(_ localFilesRequest, changes []core.FileChange, err error) {
	// 刷新后尽量恢复原来的选中项，避免每次切 tab 都跳回第一项。
	// 目录节点按完整路径恢复，这样 stage/unstage 目录后光标会跟到另一组。
	previous := t.localChange()
	target := ""
	if previous.Path == "" {
		target = folderPath(t.changeList.GetPath(t.changeList.GetCurrentNode()))
	}

	title := " local changes "
	var changeRoot *tview.TreeNode
	switch {
	case err != nil:
		changeRoot = tview.NewTreeNode("")
		changeRoot.AddChild(tview.NewTreeNode(colorDanger.markup("error") + " " + tview.Escape(err.Error())))
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
		// 失败由 Task 记录，下次请求本地变更时会重试。
		t.clearLocalDiff("failed to load local changes")
	case len(changes) == 0:
		t.clearLocalDiff("no changes")
	default:
		node := findLocalNode(changeRoot, previous)
		if node == nil && target != "" {
			node = findLocalFolder(changeRoot, target)
		}
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
		// 目录节点提示选择文件；错误/无变更占位节点保留当前内容
		if _, isFolder := node.GetReference().(folder); isFolder {
			t.clearLocalDiff("select a file")
		}
		return
	}
	t.tasks.localDiff.Execute(localDiffRequest{
		change:  change,
		options: t.diffOptions(t.changeDiff),
	})
}

// showLocalDiffRunning 保留旧 diff，只通过 statusBar 提示加载。
func (t *TUI) showLocalDiffRunning(req localDiffRequest) {
	t.statusStart("local-diff", "loading diff "+req.change.Path)
}

func (t *TUI) showLocalDiff(req localDiffRequest, text string, err error) {
	message := "diff: " + req.change.Path
	switch {
	case err != nil:
		text = colorDanger.markup("error") + " " + tview.Escape(err.Error())
		message = "diff failed: " + req.change.Path
	case text == "":
		text = colorMuted.markup("(no diff)")
		message = "no diff: " + req.change.Path
	}
	t.setDiff(t.changeDiff, "local-diff", req.change, text, message)
}

// clearLocalDiff 清空 diff 并让在途请求失效；Task 会取消对应的 git 进程。
func (t *TUI) clearLocalDiff(message string) {
	t.tasks.localDiff.Cancel()
	t.clearDiff(t.changeDiff, "local-diff", message)
}

/** HISTORY */

// requestHistory 请求 path 的文件历史；相同路径的重复请求由 Task 去重。
func (t *TUI) requestHistory(path string) {
	t.tasks.fileHistory.Execute(historyRequest{path: path})
}

// showHistory 用文件历史填充弹窗左侧列表，默认选中第一条并请求 diff。
func (t *TUI) showHistory(_ historyRequest, commits []core.Commit, err error) {
	if !t.historyVisible || t.historyPopup == nil || t.historyView == nil {
		return
	}
	t.historyPopup.setCommits(commits)

	switch {
	case err != nil:
		t.tasks.fileHistoryDiff.Cancel()
		t.historyView.SetError(err.Error())
	case len(commits) == 0:
		t.tasks.fileHistoryDiff.Cancel()
		t.historyView.SetCommits(nil)
	default:
		t.historyView.SetCommits(commits)
	}
}

/** HISTORY DIFF */

func (t *TUI) requestHistoryDiff(commit core.Commit) {
	if t.historyPopup == nil || t.historyView == nil {
		return
	}
	options := core.DiffOptions{
		IgnoreWhitespace: t.IgnoreWhitespace,
		SideBySide:       t.SideBySide,
		Colorizers:       t.Colorizers,
		Width:            t.historyView.width,
	}
	t.tasks.fileHistoryDiff.Execute(historyDiffRequest{
		commit:  commit,
		change:  core.FileChange{Path: t.historyPopup.change.Path},
		options: options,
	})
}

func (t *TUI) showHistoryDiff(req historyDiffRequest, text string, err error) {
	if !t.historyVisible || t.historyView == nil {
		return
	}
	switch {
	case err != nil:
		text = colorDanger.markup("error") + " " + tview.Escape(err.Error())
	case text == "":
		text = colorMuted.markup("(no diff)")
	}
	t.historyView.SetDiff(req.change.Path, text)
}

// syncHistoryDiffWidth 在弹窗内宽变化时按新宽度重新请求 diff。
func (t *TUI) syncHistoryDiffWidth() {
	if !t.historyVisible || t.historyView == nil {
		return
	}
	t.refreshHistoryDiff()
}

/** DIFF */

func (t *TUI) setDiff(view *diffView, slot string, change core.FileChange, text, message string) {
	t.statusDone(slot, message)
	view.SetTitle(" file diff: " + tview.Escape(change.Path) + " ")
	view.SetText(text)
	view.ScrollToBeginning()
}

func (t *TUI) clearDiff(view *diffView, slot, message string) {
	t.statusDone(slot, message)
	view.SetTitle(" file diff ")
	if message == "" {
		view.SetText("")
	} else {
		view.SetText(colorMuted.markup("(" + tview.Escape(message) + ")"))
	}
	view.ScrollToBeginning()
}

// diffView 返回当前显示的 diff 面板；history 弹窗打开时返回弹窗内的 diff。
func (t *TUI) diffView() *diffView {
	if t.historyVisible {
		return t.historyView.diff
	}
	if t.activeTab == tabChange {
		return t.changeDiff
	}
	return t.fileDiff
}

// diffOptions 按当前状态和 diff 面板宽度动态组装展示选项。
func (t *TUI) diffOptions(view *diffView) core.DiffOptions {
	options := core.DiffOptions{
		IgnoreWhitespace: t.IgnoreWhitespace,
		SideBySide:       t.SideBySide,
		Colorizers:       t.Colorizers,
	}
	if view != nil {
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

// syncDiffViewWidth 在面板内宽变化时按新宽度重新请求 diff。
// 由 diff 面板的 DrawFunc 调用，此时 Flex 已设置好本帧的 rect。
func (t *TUI) syncDiffViewWidth(view *diffView) {
	_, _, width, _ := view.GetInnerRect()
	if width <= 0 || width == t.diffWidth {
		return
	}
	t.diffWidth = width
	t.refreshActiveDiff()
}

/** CHECKOUT */

// executeCheckout 通过节点引用取分支或标签并强制执行，保证每次按空格都真正执行 git checkout。
func (t *TUI) executeCheckout(node *tview.TreeNode) {
	switch ref := node.GetReference().(type) {
	case core.Branch:
		t.tasks.checkout.Execute(checkoutRequest{branch: ref})
	case core.Tag:
		t.tasks.checkout.Execute(checkoutRequest{tag: ref})
	}
}

func (t *TUI) showCheckoutRunning(req checkoutRequest) {
	t.statusStart("checkout", "checking out "+req.name())
}

func (t *TUI) showCheckout(_ checkoutRequest, ref string, err error) {
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message := strings.Join(strings.Fields(err.Error()), " ")
		t.statusDone("checkout", "checkout failed: "+message)
		return
	}
	t.statusDone("checkout", "checkout "+ref)

	// refs 刷新后选中刚检出的引用并定位到新的 HEAD 提交；
	// commit 列表里的 HEAD 标记也随 checkout 变化。
	t.pendingRef = ref
	t.pendingHead = true
	// commit list 只在 All Commits tab 里可见，checkout 后切过去并聚焦。
	if t.activeTab != tabCommit {
		t.switchTab(tabCommit)
	} else {
		t.reloadRefs()
		t.reloadCommits()
	}
	t.app.SetFocus(t.commitList)
}

/** CREATE */

// executeCreate 通过 Execute 强制执行，保证每次创建都真正执行 git 命令。
func (t *TUI) executeCreate(op createOp, name string, commit *core.Commit) {
	t.tasks.create.Execute(createRequest{op: op, name: name, commit: commit})
}

func (t *TUI) showCreateRunning(req createRequest) {
	t.statusStart("create", t.createOpLabel(req, false))
}

func (t *TUI) showCreate(req createRequest, ref string, err error) {
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message := strings.Join(strings.Fields(err.Error()), " ")
		prefix := "create branch failed: "
		if req.op == opCreateTag {
			prefix = "create tag failed: "
		}
		t.statusDone("create", prefix+message)
		return
	}

	t.statusDone("create", t.createOpLabel(req, true))

	// refs 刷新后选中刚创建的引用；commit 列表里的 HEAD 标记也随 checkout 变化。
	t.pendingRef = ref
	t.reloadRefs()
	t.reloadCommits()
}

// createOpLabel 返回创建操作的进行中/完成文案。
func (t *TUI) createOpLabel(req createRequest, done bool) string {
	noun := "branch"
	if req.op == opCreateTag {
		noun = "tag"
	}
	if done {
		return "created " + noun + " " + req.name
	}
	return "creating " + noun + " " + req.name
}

/** DELETE */

// executeDelete 通过 Execute 强制执行，保证每次删除都真正执行 git 命令。
func (t *TUI) executeDelete(req deleteRequest) {
	t.tasks.delete.Execute(req)
}

func (t *TUI) showDeleteRunning(req deleteRequest) {
	t.statusStart("delete", t.deleteOpLabel(req, false))
}

func (t *TUI) showDelete(req deleteRequest, _ string, err error) {
	if err != nil {
		// git 的错误可能是多行，压成一行避免撑坏单行 statusBar。
		message := strings.Join(strings.Fields(err.Error()), " ")
		t.statusDone("delete", "delete failed: "+message)
		return
	}
	t.statusDone("delete", t.deleteOpLabel(req, true))

	// 删除引用后分支/标签列表和 commit 图都可能变化。
	t.reloadRefs()
	t.reloadCommits()
}

// deleteOpLabel 返回删除操作的进行中/完成文案。
func (t *TUI) deleteOpLabel(req deleteRequest, done bool) string {
	noun := "branch "
	switch {
	case req.tag.Name != "":
		noun = "tag "
	case req.branch.Remote:
		noun = "remote branch "
	}
	if done {
		return "deleted " + noun + req.name()
	}
	return "deleting " + noun + req.name()
}

/** SYNC OP */

// executeSync 通过 Execute 强制执行，保证每次按键都真正执行 git 命令。
func (t *TUI) executeSync(op syncOp) {
	t.tasks.sync.Execute(syncOpRequest{op: op})
}

func (t *TUI) showSyncRunning(req syncOpRequest) {
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
		case opPush, opPushForce:
			message = "push failed: " + message
		default:
			message = "fetch failed: " + message
		}
	}
	t.statusDone("sync", message)

	// pull 成功后 HEAD 会前进，等 refs 刷新拿到新的 HeadHash 再选中它。
	if req.op == opPull && err == nil {
		t.pendingHead = true
	}

	// pull 失败也可能留下冲突或已更新的 refs，统一按当前 tab 刷新。
	t.refreshTab(t.activeTab)

	// rebase / amend 后历史分叉，push 会因非快进而被拒；提示改用 force push。
	if req.op == opPush && errors.Is(err, core.ErrNonFastForward) {
		t.promptForcePush()
	}
}

// syncOpLabel 返回同步操作的进行中/完成文案。
func (t *TUI) syncOpLabel(req syncOpRequest, done bool) string {
	switch req.op {
	case opPull:
		if done {
			return "pulled " + t.HeadRef
		}
		return "pulling " + t.HeadRef
	case opPush, opPushForce:
		if done {
			return "pushed " + t.HeadRef
		}
		return "pushing " + t.HeadRef
	default:
		if done {
			return "fetched"
		}
		return "fetching"
	}
}

// promptForcePush 在 push 因非快进被拒后弹窗确认，确认后用 force push 重推当前分支。
func (t *TUI) promptForcePush() {
	// 已有弹窗时只保留 statusBar 的错误信息，避免叠出多个弹窗。
	if t.root == nil || t.popupVisible() {
		return
	}

	message := fmt.Sprintf("The remote branch has commits that are not in your local branch.\n\nForce push %s to its upstream?\n\n",
		tview.Escape(t.HeadRef))

	message += colorDanger.markup("This rewrites remote history and cannot be undone.")

	const page = "force-push"

	prompt := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Force Push"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			t.promptVisible = false
			t.closePopup(page)
			if buttonIndex == 1 {
				t.executeSync(opPushForce)
			}
		})

	t.setupPrompt(page, prompt)
}

// startSyncSpinner 启动当前分支前的点阵动画；已有动画时不重复启动。
func (t *TUI) startSyncSpinner() {
	if t.syncStop != nil || t.app == nil {
		return
	}
	stop := make(chan struct{})
	t.syncStop = stop
	t.syncFrame = 0
	t.renderSyncIndicator()

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				t.app.QueueUpdateDraw(func() {
					// 排队期间同步操作可能已经结束，停止后不再改动分支文本。
					select {
					case <-stop:
						return
					default:
					}
					t.syncFrame = (t.syncFrame + 1) % len(syncFrames)
					t.renderSyncIndicator()
				})
			}
		}
	}()
}

// stopSyncSpinner 停止动画并把当前分支前的标记恢复成对勾。
func (t *TUI) stopSyncSpinner() {
	if t.syncStop != nil {
		close(t.syncStop)
		t.syncStop = nil
	}
	t.syncFrame = -1
	t.renderSyncIndicator()
}

// renderSyncIndicator 按当前动画帧更新 HEAD 所在的本地分支节点标记；
// HEAD 对应标签或其他 ref 时没有可更新的分支节点。
func (t *TUI) renderSyncIndicator() {
	node := t.refNode(t.HeadRef)
	if node == nil {
		return
	}
	branch, ok := node.GetReference().(core.Branch)
	if !ok {
		return
	}
	node.SetText(branchLabel(branch, t.HeadRef, t.syncFrame))
}

/** COMMIT OP */

// executeReset 通过 Execute 强制执行，把当前分支重置到 commit。
func (t *TUI) executeReset(commit core.Commit, mode core.ResetMode) {
	t.tasks.commit.Execute(commitOpRequest{op: opReset, commit: commit, resetMode: mode})
}

// executeRebase 通过 Execute 强制执行，把当前分支 rebase 到 commit。
func (t *TUI) executeRebase(commit core.Commit) {
	t.tasks.commit.Execute(commitOpRequest{op: opRebase, commit: commit})
}

// executeRebaseInteractive 通过 Execute 强制执行 interactive rebase。
func (t *TUI) executeRebaseInteractive(commit core.Commit, actions map[string]core.RebaseAction) {
	t.tasks.commit.Execute(commitOpRequest{op: opRebaseInteractive, commit: commit, rebaseActions: actions})
}

// executeReword 通过 Execute 强制执行，改写 commit 的提交信息。
func (t *TUI) executeReword(commit core.Commit, subject, description string) {
	t.tasks.commit.Execute(commitOpRequest{op: opReword, commit: commit, subject: subject, description: description})
}

// executeMerge 通过 Execute 强制执行，把 commit 合入当前分支。
func (t *TUI) executeMerge(commit core.Commit, mode core.MergeMode) {
	t.tasks.commit.Execute(commitOpRequest{op: opMerge, commit: commit, mergeMode: mode})
}

// executeCherryPick 通过 Execute 强制执行，把 commit cherry-pick 到当前分支。
func (t *TUI) executeCherryPick(commit core.Commit) {
	t.tasks.commit.Execute(commitOpRequest{op: opCherryPick, commit: commit})
}

// executeCommit 通过 Execute 强制执行，用表单内容提交暂存区改动。
func (t *TUI) executeCommit(subject, description string, amend bool) {
	t.tasks.commit.Execute(commitOpRequest{
		op:          opCommit,
		subject:     subject,
		description: description,
		amend:       amend,
	})
}

// executeCommitOp 执行只带操作类型的 continue / abort。
func (t *TUI) executeCommitOp(op commitOp) {
	t.tasks.commit.Execute(commitOpRequest{op: op})
}

func (t *TUI) showCommitOpRunning(req commitOpRequest) {
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
		case opRebase:
			message = "rebase failed: " + message
		case opRebaseInteractive:
			message = "interactive rebase failed: " + message
		case opReword:
			message = "reword failed: " + message
		case opRebaseContinue:
			message = "rebase continue failed: " + message
		case opRebaseAbort:
			message = "rebase abort failed: " + message
		case opMerge:
			if req.mergeMode == core.MergeSquash {
				message = "squash failed: " + message
			} else {
				message = "merge failed: " + message
			}
		case opMergeContinue:
			message = "merge continue failed: " + message
		case opMergeAbort:
			message = "merge abort failed: " + message
		case opSquashContinue:
			message = "squash continue failed: " + message
		case opSquashAbort:
			message = "squash abort failed: " + message
		case opCherryPick:
			message = "cherry-pick failed: " + message
		case opCherryPickContinue:
			message = "cherry-pick continue failed: " + message
		case opCherryPickAbort:
			message = "cherry-pick abort failed: " + message
		default:
			message = "commit failed: " + message
		}
	}
	t.statusDone("commit", message)

	// 提交成功后清空表单，方便继续下一次提交。
	if err == nil && req.op == opCommit {
		t.commitSubject.SetText("", false)
		t.commitDescription.SetText("", false)
		t.amendCheck.SetChecked(false)
		if t.activeTab == tabChange {
			t.app.SetFocus(t.changeList)
		}
	}

	// reset/rebase 会移动 HEAD，并可能改变索引和工作区；rebase 冲突时还会留下中间状态。
	t.refreshTab(t.activeTab)
}

// commitOpLabel 返回 commit 操作的进行中/完成文案。
func (t *TUI) commitOpLabel(req commitOpRequest, done bool) string {
	switch req.op {
	case opReset:
		if done {
			return "reset --" + req.resetMode.String() + " " + req.commit.ShortHash()
		}
		return "resetting --" + req.resetMode.String() + " " + req.commit.ShortHash()
	case opRebase:
		if done {
			return "rebased onto " + req.commit.ShortHash()
		}
		return "rebasing onto " + req.commit.ShortHash()
	case opRebaseInteractive:
		if done {
			return "interactively rebased onto " + req.commit.ShortHash()
		}
		return "interactively rebasing onto " + req.commit.ShortHash()
	case opReword:
		if done {
			return "reworded " + req.commit.ShortHash()
		}
		return "rewording " + req.commit.ShortHash()
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
	case opMerge:
		if req.mergeMode == core.MergeSquash {
			if done {
				return "squashed " + req.commit.ShortHash()
			}
			return "squashing " + req.commit.ShortHash()
		}
		if done {
			return "merged " + req.commit.ShortHash() + " (" + req.mergeMode.String() + ")"
		}
		return "merging " + req.commit.ShortHash() + " (" + req.mergeMode.String() + ")"
	case opMergeContinue:
		if done {
			return "merge continued"
		}
		return "continuing merge"
	case opMergeAbort:
		if done {
			return "merge aborted"
		}
		return "aborting merge"
	case opSquashContinue:
		if done {
			return "squash committed"
		}
		return "committing squash"
	case opSquashAbort:
		if done {
			return "squash aborted"
		}
		return "aborting squash"
	case opCherryPick:
		if done {
			return "cherry-picked " + req.commit.ShortHash()
		}
		return "cherry-picking " + req.commit.ShortHash()
	case opCherryPickContinue:
		if done {
			return "cherry-pick continued"
		}
		return "continuing cherry-pick"
	case opCherryPickAbort:
		if done {
			return "cherry-pick aborted"
		}
		return "aborting cherry-pick"
	default:
		if done {
			if req.amend {
				return "amended commit"
			}
			return "committed changes"
		}
		if req.amend {
			return "amending commit"
		}
		return "committing changes"
	}
}

/** CHANGE OP */

func (t *TUI) executeChangeOp(op changeOp, changes []core.FileChange, path string) {
	t.tasks.change.Execute(changeOpRequest{op: op, changes: changes, path: path})
}

func (t *TUI) showChangeOpRunning(req changeOpRequest) {
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

// changeOpLabel 返回本地变更操作的进行中/完成文案。
func (t *TUI) changeOpLabel(req changeOpRequest, done bool) string {
	path := req.path
	if len(req.changes) > 1 {
		path = fmt.Sprintf("%s (%d files)", req.path, len(req.changes))
	}
	switch req.op {
	case opStage:
		if done {
			return "staged " + path
		}
		return "staging " + path
	case opUnstage:
		if done {
			return "unstaged " + path
		}
		return "unstaging " + path
	default:
		if done {
			return "discarded " + path
		}
		return "discarding " + path
	}
}

/** STATUS */

// statusStart 标记一个槽位正在执行；同一槽位重复调用只更新文案。
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

// statusDone 结束槽位的进行中状态；message 非空时同时更新最近完成信息。
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
		t.statusBar.SetText(colorMuted.markup(tview.Escape(t.statusMsg)))
		return
	}

	messages := make([]string, 0, len(t.statusBusy))
	for _, message := range t.statusBusy {
		messages = append(messages, tview.Escape(message))
	}
	sort.Strings(messages)
	t.statusBar.SetText(colorMuted.markup(strings.Join(messages, ", ") + "…"))
}

// renderInProgressActions 按 rebase / merge / squash / cherry-pick 状态切换 statusBar 右侧 Continue / Abort 的文案和动作。
// 这些操作不会同时进行；同时处于未完成状态时按 rebase、merge、squash、cherry-pick 的顺序优先显示。
func (t *TUI) renderInProgressActions() {
	if t.statusRow == nil || t.keybindHint == nil || t.continueButton == nil || t.abortButton == nil {
		return
	}

	var continueLabel, abortLabel string
	var continueFunc, abortFunc func()
	switch {
	case t.Rebasing:
		continueLabel, abortLabel = "Continue rebase", "Abort rebase"
		continueFunc, abortFunc = t.continueRebase, t.abortRebase
	case t.Merging:
		continueLabel, abortLabel = "Continue merge", "Abort merge"
		continueFunc, abortFunc = t.continueMerge, t.abortMerge
	case t.Squashing:
		continueLabel, abortLabel = "Continue squash", "Abort squash"
		continueFunc, abortFunc = t.continueSquash, t.abortSquash
	case t.CherryPicking:
		continueLabel, abortLabel = "Continue cherry-pick", "Abort cherry-pick"
		continueFunc, abortFunc = t.continueCherryPick, t.abortCherryPick
	}
	t.continueButton.SetLabel(continueLabel).SetSelectedFunc(continueFunc)
	t.abortButton.SetLabel(abortLabel).SetSelectedFunc(abortFunc)

	continueWidth, abortWidth := 0, 0
	if continueLabel != "" {
		continueWidth = tview.TaggedStringWidth(continueLabel) + 2
		abortWidth = tview.TaggedStringWidth(abortLabel) + 2
	}
	t.statusRow.ResizeItem(t.continueButton, continueWidth, 0)
	t.statusRow.ResizeItem(t.abortButton, abortWidth, 0)

	// 没有未完成操作时在同样的位置显示快捷键提示。
	hintWidth := 0
	if continueLabel == "" {
		hintWidth = tview.TaggedStringWidth(t.keybindHint.GetText(true)) + 2
	}
	t.statusRow.ResizeItem(t.keybindHint, hintWidth, 0)
}
