package tui

import (
	"sort"
	"strings"

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
