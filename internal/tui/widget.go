package tui

import (
	"fmt"
	"sort"
	"strings"

	"lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// folder 是树中的可折叠目录节点；key 是目录标识，name 是显示名。
type folder struct {
	key  string
	name string
}

// folderLabel 返回目录节点的显示文本。
func folderLabel(folder folder, expanded bool) string {
	if expanded {
		return "▼ " + tview.Escape(folder.name)
	}
	return "▶ " + tview.Escape(folder.name)
}

// setFolderExpanded 同步目录节点的展开状态和箭头图标。
func setFolderExpanded(node *tview.TreeNode, expanded bool) {
	node.SetExpanded(expanded)
	if folder, ok := node.GetReference().(folder); ok {
		node.SetText(folderLabel(folder, expanded))
	}
}

// modalForm 是居中的输入弹窗，布局和交互方式类似 tview.Modal。
// SetText 需要先于 SetLabel 调用，保证提示文字在输入框上方。
type modalForm struct {
	*tview.Form

	width   int
	text    *formText
	input   *tview.InputField
	buttons []string
	done    func(buttonIndex int, buttonLabel string)
}

func newModalForm(width int) *modalForm {
	f := &modalForm{width: width}
	f.Form = tview.NewForm().
		SetButtonsAlign(tview.AlignCenter)
	f.SetLabelColor(colorMuted.cell).
		SetFieldTextColor(tview.Styles.PrimaryTextColor).
		SetFieldBackgroundColor(colorPopup.cell).
		SetButtonBackgroundColor(colorPopup.cell).
		SetButtonActivatedStyle(tcell.StyleDefault.
			Background(colorText.cell).
			Foreground(colorOnAccent.cell)).
		SetBackgroundColor(colorPopup.cell).
		SetBorder(true).
		SetBorderPadding(1, 1, 2, 2).
		SetBorderStyle(tcell.StyleDefault.
			Foreground(tview.Styles.BorderColor).
			Background(colorPopup.cell))

	// 焦点在按钮上时 Esc 也要能关闭弹窗。
	f.SetCancelFunc(func() {
		f.finish(-1, "")
	})
	return f
}

func (f *modalForm) Draw(screen tcell.Screen) {
	screenWidth, screenHeight := screen.Size()
	width := min(f.width, screenWidth)
	height := f.height()
	x := (screenWidth - width) / 2
	y := (screenHeight - height) / 2
	f.SetRect(x, y, width, height)
	f.Form.Draw(screen)
}

// SetText 设置输入框上方的提示文字；text 中的 tview 标签会被转义。
func (f *modalForm) SetText(text string) *modalForm {
	// 可用宽度 = 弹窗宽度 - 左右边框各 1 列 - 左右 border padding 各 2 列。
	lines := tview.WordWrap(tview.Escape(text), f.width-6)
	if f.text == nil {
		f.text = &formText{TextView: tview.NewTextView().
			SetDynamicColors(true).
			SetScrollable(false).
			SetTextAlign(tview.AlignCenter)}
		f.AddFormItem(f.text)
	}
	f.text.SetText(strings.Join(lines, "\n"))
	f.text.SetSize(len(lines), 0)
	return f
}

func (f *modalForm) SetLabel(label string) *modalForm {
	f.input = tview.NewInputField().
		SetLabel(label).
		SetFieldWidth(30)
	f.input.SetBackgroundColor(colorPopup.cell)
	f.input.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			f.accept()
			return nil
		case tcell.KeyEscape:
			f.finish(-1, "")
			return nil
		}
		return event
	})
	f.AddFormItem(f.input)
	return f
}

func (f *modalForm) SetButtons(labels []string) *modalForm {
	f.ClearButtons()
	f.buttons = labels
	for index, label := range labels {
		f.AddButton(label, func() {
			f.finish(index, label)
		})
		// 和 tview.Modal 一样，方向键在按钮之间移动。
		button := f.GetButton(f.GetButtonCount() - 1)
		button.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyDown, tcell.KeyRight:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			case tcell.KeyUp, tcell.KeyLeft:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			}
			return event
		})
	}
	return f
}

func (f *modalForm) SetDoneFunc(handler func(buttonIndex int, buttonLabel string)) *modalForm {
	f.done = handler
	return f
}

// GetInputText 返回输入框当前内容。
func (f *modalForm) GetInputText() string {
	if f.input == nil {
		return ""
	}
	return f.input.GetText()
}

// accept 触发最后一个按钮，作为输入框回车时的默认操作。
func (f *modalForm) accept() {
	if len(f.buttons) > 0 {
		f.finish(len(f.buttons)-1, f.buttons[len(f.buttons)-1])
	}
}

func (f *modalForm) finish(buttonIndex int, buttonLabel string) {
	if f.done != nil {
		f.done(buttonIndex, buttonLabel)
	}
}

// height 按 Form 的垂直布局计算弹窗高度：边框和 padding、各表单项及间距、按钮行。
func (f *modalForm) height() int {
	height := 4
	items := 0
	if f.text != nil {
		height += f.text.GetFieldHeight()
		items++
	}
	if f.input != nil {
		height++
		items++
	}
	height += items
	if len(f.buttons) > 0 {
		height++
	}
	return height
}

// formText 是表单里的多行只读文本，忽略表单的 label 列，按整行宽度显示。
type formText struct {
	*tview.TextView
}

func (m *formText) SetFormAttributes(_ int, labelColor, bgColor, fieldTextColor, fieldBgColor tcell.Color) tview.FormItem {
	return m.TextView.SetFormAttributes(0, labelColor, bgColor, fieldTextColor, fieldBgColor)
}

// listView 把视口偏移和选中项分开，使滚轮只滚动内容。
// tview.List 的 Draw 会强制让选中项保持可见，无法做到这一点。
// 每个 item 可以包含多行，选中和高亮按 item 生效。
type listView struct {
	*tview.Box

	items  [][]string
	total  int   // 所有 item 和间距的总行数
	starts []int // 每个 item 第一行的绝对行号
	gap    int   // 相邻 item 之间的空行数

	offset         int
	marked         int
	current        int
	pendingVisible bool

	selected       func()
	selectedStyle  tcell.Style
	highlight      func(index int)
	highlightStyle tcell.Style
	activated      func()
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
	// 布局前调用 SetCurrentItem 时无法滚动，等首次绘制有了尺寸再补上。
	if v.pendingVisible {
		v.ensureVisible()
	} else {
		v.clampOffset()
	}

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
			tview.Print(screen, line, x, y+row, width, tview.AlignLeft, colorText.cell)
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
			v.SetCurrentItem(0)
		case tcell.KeyEnd:
			v.SetCurrentItem(len(v.items) - 1)
		case tcell.KeyEnter:
			if v.activated != nil {
				v.activated()
			}
		case tcell.KeyRune:
			// 与 tview.TreeView 保持一致：j/k 移动，g/G 跳到首尾。
			switch event.Rune() {
			case 'j':
				v.move(1)
			case 'k':
				v.move(-1)
			case 'g':
				v.SetCurrentItem(0)
			case 'G':
				v.SetCurrentItem(len(v.items) - 1)
			}
		}
	})
}

func (v *listView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return v.Box.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		if !v.InRect(event.Position()) {
			return false, nil
		}
		switch action {
		case tview.MouseLeftDown:
			setFocus(v)
			consumed = true
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
						v.SetCurrentItem(index)
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

// SetItems 替换全部 item，并重新计算每个 item 的起始行号。
func (v *listView) SetItems(items [][]string) {
	v.items = items
	v.starts = make([]int, len(items))
	v.total = 0
	for i, item := range items {
		v.starts[i] = v.total
		v.total += len(item)
		if i < len(items)-1 {
			v.total += v.gap
		}
	}

	v.current = min(max(v.current, 0), max(0, len(items)-1))
	if v.marked >= len(items) {
		v.marked = -1
	}
	v.clampOffset()
}

// SetLines 是单行 item 的快捷方式，供 commit 列表使用。
func (v *listView) SetLines(lines []string) {
	items := make([][]string, len(lines))
	for i, line := range lines {
		items[i] = []string{line}
	}
	v.SetItems(items)
}

// SetGap 设置相邻 item 之间的空行数；已有 item 会按新间距重新布局。
func (v *listView) SetGap(gap int) {
	if v.gap == gap {
		return
	}
	v.gap = gap
	v.SetItems(v.items)
}

// SetMarkedItem 标记对比基准所在 item；index 越界时清除标记。
func (v *listView) SetMarkedItem(index int) {
	if index < 0 || index >= len(v.items) {
		v.marked = -1
		return
	}
	v.marked = index
}

func (v *listView) SetCurrentItem(index int) {
	if len(v.items) == 0 {
		return
	}
	v.current = min(max(index, 0), len(v.items)-1)
	v.ensureVisible()
	if v.selected != nil {
		v.selected()
	}
}

func (v *listView) SetSelectedFunc(handler func()) {
	v.selected = handler
}

func (v *listView) SetHighlightFunc(handler func(index int)) {
	v.highlight = handler
}

func (v *listView) SetActivatedFunc(handler func()) {
	v.activated = handler
}

func (v *listView) CurrentItem() int {
	return v.current
}

func (v *listView) ensureVisible() {
	_, _, _, height := v.GetInnerRect()
	if height <= 0 {
		v.pendingVisible = true
		return
	}
	v.pendingVisible = false
	if v.current < 0 || v.current >= len(v.items) {
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
		if i < len(v.items)-1 {
			lines += v.gap
		}
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
	v.SetCurrentItem(index)
}

// rebaseForm 是 interactive rebase 弹窗，布局为说明、动作列表、提示和按钮三段。
// 动作状态和 rebase 规则由调用方维护，这里只负责展示和交互转发。
type rebaseForm struct {
	view         *tview.Flex
	list         *listView
	lines        []string // renderGraph 生成的、不含动作列的行
	hint         *tview.TextView
	cancelButton *tview.Button
	rebaseButton *tview.Button
}

func newRebaseForm(
	message string,
	lines []string,
	actions []core.RebaseAction,
	selectedStyle tcell.Style,
	borderColor, focusColor tcell.Color,
	onCycleAction func(delta int),
	onCancel, onConfirm func(),
) *rebaseForm {
	f := &rebaseForm{lines: lines}

	buttonStyle := tcell.StyleDefault.
		Foreground(colorText.cell).
		Background(colorPopup.cell)
	buttonActivatedStyle := tcell.StyleDefault.
		Foreground(colorOnAccent.cell).
		Background(colorText.cell)
	borderStyle := tcell.StyleDefault.
		Foreground(borderColor).
		Background(colorPopup.cell)

	setFocusBorder := func(box *tview.Box, focused bool) {
		color := borderColor
		if focused {
			color = focusColor
		}
		box.SetBorderColor(color).SetTitleColor(color)
	}

	// 顶部操作说明与现有 rebase 弹窗保持一致。
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetTextAlign(tview.AlignCenter).
		SetText(message)
	header.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetBorderStyle(borderStyle)
	header.SetBackgroundColor(colorPopup.cell)

	// 中间列表复用 commit list 的渲染和交互，只在每行最左侧加一列动作。
	list := newListView(selectedStyle, tcell.StyleDefault)
	list.SetTitle(" commits ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(list.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(list.Box, false) }).
		SetBackgroundColor(colorPopup.cell).
		SetBorderStyle(borderStyle)
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			onCycleAction(-1)
			return nil
		case tcell.KeyRight:
			onCycleAction(1)
			return nil
		}
		return event
	})

	// 底部与 rebase 弹窗一样上下排列：提示在上，按钮在下并居中。
	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	hint.SetBackgroundColor(colorPopup.cell)

	cancelButton := tview.NewButton("Cancel").
		SetSelectedFunc(onCancel)
	rebaseButton := tview.NewButton("Rebase").
		SetSelectedFunc(onConfirm)
	for _, button := range []*tview.Button{cancelButton, rebaseButton} {
		button.SetStyle(buttonStyle).SetActivatedStyle(buttonActivatedStyle)
	}

	buttons := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(cancelButton, len("Cancel")+6, 0, false).
		AddItem(nil, 2, 0, false).
		AddItem(rebaseButton, len("Rebase")+6, 0, false).
		AddItem(nil, 0, 1, false)

	footer := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(hint, 1, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(buttons, 1, 0, false)
	// Flex 默认不清空背景，nil 行和按钮两侧会露出下面的主界面；
	// 换成普通 Box 让 footer 自己铺满弹窗底色。
	footer.Box = tview.NewBox()
	footer.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetBackgroundColor(colorPopup.cell).
		SetBorderStyle(borderStyle)

	parts := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 5, 0, false).
		AddItem(list, 0, 1, true).
		AddItem(footer, 5, 0, false)

	inner := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(nil, 8, 0, false).
		AddItem(parts, 0, 1, true).
		AddItem(nil, 8, 0, false)

	// 弹窗铺满整个界面，左右各留 15 列、上下各留 3 行，和 history 弹窗一致。
	view := tview.NewFlex().
		AddItem(nil, 35, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 35, 0, false)

	// 只有三个分区和按钮接收鼠标；其余区域直接消费，避免点击穿透到下面的主界面。
	view.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if header.InRect(event.Position()) || list.InRect(event.Position()) || footer.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	f.view = view
	f.list = list
	f.hint = hint
	f.cancelButton = cancelButton
	f.rebaseButton = rebaseButton

	f.ResetHint()
	f.SetActions(actions)

	// 构造时不会触发 focus 回调，先手动初始化列表的聚焦边框。
	setFocusBorder(list.Box, true)

	return f
}

// SetActions 按 actions 重绘动作列；线的部分保持不变。
func (f *rebaseForm) SetActions(actions []core.RebaseAction) {
	rows := make([]string, len(f.lines))
	for i, line := range f.lines {
		label := fmt.Sprintf("%-7s", string(actions[i]))
		color := colorSuccess
		switch actions[i] {
		case core.RebaseSquash:
			color = colorSecondary
		case core.RebaseFixup:
			color = colorAccent
		case core.RebaseDrop:
			color = colorDanger
		}
		rows[i] = color.markup(label) + " " + line
	}
	f.list.SetLines(rows)
}

// ResetHint 恢复默认的底部提示。
func (f *rebaseForm) ResetHint() {
	f.hint.SetText(colorMuted.markup(tview.Escape("Local changes will be stashed and restored automatically.")))
}

// SetErrorHint 用危险色显示校验错误。
func (f *rebaseForm) SetErrorHint(message string) {
	f.hint.SetText(colorDanger.markup(tview.Escape(message)))
}

// focusables 返回 Tab 循环的焦点顺序。
func (f *rebaseForm) focusables() []tview.Primitive {
	return []tview.Primitive{f.list, f.cancelButton, f.rebaseButton}
}

// historyView 是文件历史弹窗：左侧 commit 列表，右侧文件 diff。
type historyView struct {
	view  *tview.Flex
	list  *listView
	diff  *tview.TextView
	width int // side-by-side 时 diff 的渲染宽度
}

func newHistoryView(
	path string,
	selectedStyle tcell.Style,
	borderColor, focusColor tcell.Color,
	onSelect, onActivate func(),
	onResize func(),
	onToggleWhitespace, onToggleSideBySide func(),
) *historyView {
	v := &historyView{}

	borderStyle := tcell.StyleDefault.
		Foreground(borderColor).
		Background(colorPopup.cell)

	setFocusBorder := func(box *tview.Box, focused bool) {
		color := borderColor
		if focused {
			color = focusColor
		}
		box.SetBorderColor(color).SetTitleColor(color)
	}

	list := newListView(selectedStyle, tcell.StyleDefault)
	// item 之间留一个空行，选中背景只覆盖 item 自身。
	list.SetGap(1)

	list.SetTitle(" history ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(list.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(list.Box, false) }).
		SetBackgroundColor(colorPopup.cell).
		SetBorderStyle(borderStyle)

	list.SetSelectedFunc(onSelect)
	list.SetActivatedFunc(onActivate)

	diff := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetText(colorMuted.markup("loading…"))

	diff.SetTitle(" file diff: "+tview.Escape(path)+" ").
		SetTitleAlign(tview.AlignLeft).
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetFocusFunc(func() { setFocusBorder(diff.Box, true) }).
		SetBlurFunc(func() { setFocusBorder(diff.Box, false) })
	// TextView 重写了 SetBackgroundColor 以同步文字背景，必须直接调用。
	diff.SetBackgroundColor(colorPopup.cell)
	diff.SetBorderStyle(borderStyle)

	diff.SetDrawFunc(func(_ tcell.Screen, _, _, _, _ int) (int, int, int, int) {
		_, _, width, _ := diff.GetInnerRect()
		if width > 0 && width != v.width {
			v.width = width
			onResize()
		}
		return diff.GetInnerRect()
	})

	diff.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'w':
			onToggleWhitespace()
			return nil
		case 's':
			onToggleSideBySide()
			return nil
		}
		return event
	})

	content := tview.NewFlex().
		AddItem(list, 40, 0, true).
		AddItem(diff, 0, 1, false)

	inner := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 3, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(nil, 3, 0, false)

	// 弹窗铺满 mainPage 所在区域，左右各留 15 列、上下各留 3 行，
	// 内容区正好是 mainPage 宽 - 30、高 - 6。
	view := tview.NewFlex().
		AddItem(nil, 15, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 15, 0, false)

	// 弹窗铺满 mainPage，只有两个子面板需要接收鼠标；其余区域直接消费，
	// 避免点击穿透到下面的主界面。
	view.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if list.InRect(event.Position()) || diff.InRect(event.Position()) {
			return action, event
		}
		return tview.MouseConsumed, nil
	})

	setFocusBorder(list.Box, true)
	setFocusBorder(diff.Box, false)

	v.view = view
	v.list = list
	v.diff = diff

	return v
}

// SetCommits 用文件历史填充左侧列表并选中第一条；空历史在 diff 面板提示。
func (v *historyView) SetCommits(commits []core.Commit) {
	if len(commits) == 0 {
		v.list.SetTitle(" history (0) ")
		v.list.SetItems(nil)
		v.diff.SetText(colorMuted.markup("(no history)"))
		return
	}

	v.list.SetTitle(fmt.Sprintf(" history (%d) ", len(commits)))
	items := make([][]string, 0, len(commits))
	for _, commit := range commits {
		header := fmt.Sprintf("%s %s",
			colorAccent.markup(tview.Escape(commit.Author)),
			colorMuted.markup(commit.Date.Format("Jan _2, 2006 15:04:05")))
		subject := " " + colorText.markup(tview.Escape(commit.Subject), true)
		items = append(items, []string{header, subject})
	}
	v.list.SetItems(items)
	v.list.SetCurrentItem(0)
}

// SetError 清空历史列表并在 diff 面板显示错误。
func (v *historyView) SetError(message string) {
	v.list.SetTitle(" history ")
	v.list.SetItems(nil)
	v.diff.SetText(colorDanger.markup("failed to load history:") + " " + tview.Escape(message))
}

// SetDiff 更新 diff 标题和正文，并回到顶部。
func (v *historyView) SetDiff(path, text string) {
	v.diff.SetTitle(" file diff: " + tview.Escape(path) + " ")
	v.diff.SetText(text)
	v.diff.ScrollToBeginning()
}

// focusables 返回 Tab 循环的焦点顺序。
func (v *historyView) focusables() []tview.Primitive {
	return []tview.Primitive{v.list, v.diff}
}
