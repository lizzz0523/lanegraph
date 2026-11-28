package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lizzz0523/lanegraph/internal/core"

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

// folderPath 把目录层级拼成完整路径。
// compactFolders 合并后的目录节点 name 已经是 a/b/c，未合并时只是单段名，两种都能拼对。
func folderPath(path []*tview.TreeNode) string {
	parts := make([]string, 0, len(path))
	for _, node := range path {
		if folder, ok := node.GetReference().(folder); ok {
			parts = append(parts, folder.name)
		}
	}
	return strings.Join(parts, "/")
}

// compactFolders 把只有一个子目录的连续目录链合并成一个节点，
// 例如 a > b > c 显示为 a/b/c，类似 IDE 的紧凑中间目录。
func compactFolders(node *tview.TreeNode) {
	for _, child := range node.GetChildren() {
		current, ok := child.GetReference().(folder)
		if !ok {
			continue
		}

		for {
			children := child.GetChildren()
			if len(children) != 1 {
				break
			}
			sub, ok := children[0].GetReference().(folder)
			if !ok {
				break
			}

			current = folder{
				key:  current.key + "/" + sub.key,
				name: current.name + "/" + sub.name,
			}
			child.SetReference(current).
				SetText(folderLabel(current, child.IsExpanded())).
				SetChildren(children[0].GetChildren())
		}

		compactFolders(child)
	}
}

// sortFoldersFirst 使用稳定排序：目录在前，同层再忽略大小写按名字字母排序；
// 忽略大小写后同名时保持原有相对顺序。
func sortFoldersFirst(node *tview.TreeNode) {
	children := node.GetChildren()
	// 文件和引用只按树中显示的最后一段名字排序，并统一转成小写忽略大小写。
	name := func(node *tview.TreeNode) string {
		var name string
		switch value := node.GetReference().(type) {
		case folder:
			return strings.ToLower(value.name)
		case core.FileChange:
			name = value.Path
		case core.Branch:
			name = value.LocalName()
		case core.Tag:
			name = value.Name
		default:
			return ""
		}
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		return strings.ToLower(name)
	}
	sort.SliceStable(children, func(i, j int) bool {
		iFolder := len(children[i].GetChildren()) > 0
		jFolder := len(children[j].GetChildren()) > 0
		if iFolder != jFolder {
			return iFolder
		}
		return name(children[i]) < name(children[j])
	})
	node.SetChildren(children)

	for _, child := range children {
		sortFoldersFirst(child)
	}
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
// 每个 item 可以包含多行，选中、对比高亮和悬停按 item 生效。
type listView struct {
	*tview.Box

	items  [][]string
	total  int   // 所有 item 和间距的总行数
	starts []int // 每个 item 第一行的绝对行号
	gap    int   // 相邻 item 之间的空行数

	hover          int
	hoverEnabled   bool
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
		hover:          -1,
		hoverEnabled:   true,
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
			if i == v.current || i == v.marked || i == v.hover {
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
			case keymap.listDown:
				v.move(1)
			case keymap.listUp:
				v.move(-1)
			case keymap.listFirst:
				v.SetCurrentItem(0)
			case keymap.listLast:
				v.SetCurrentItem(len(v.items) - 1)
			}
		}
	})
}

func (v *listView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return v.Box.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		if v.hoverEnabled && action == tview.MouseMove && v.hoverAt(event.Position()) {
			return true, nil
		}
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
			v.hover = -1
			v.offset--
			v.clampOffset()
			consumed = true
		case tview.MouseScrollDown:
			setFocus(v)
			v.hover = -1
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
	v.hover = -1
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

// SetHoverEnabled 控制鼠标悬停是否高亮 item。
func (v *listView) SetHoverEnabled(enabled bool) {
	v.hoverEnabled = enabled
	if !enabled {
		v.hover = -1
	}
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

// hoverAt 按鼠标位置更新悬停 item；返回是否发生变化。
func (v *listView) hoverAt(posX, posY int) bool {
	index := -1
	if v.InInnerRect(posX, posY) {
		_, innerY, _, _ := v.GetInnerRect()
		index = v.itemAtLine(v.offset + posY - innerY)
	}
	if index == v.hover {
		return false
	}
	v.hover = index
	return true
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

// diffView 在 TextView 之上增加鼠标悬停时的整行高亮。
// hover 是逻辑行号，-1 表示当前没有高亮。
type diffView struct {
	*tview.TextView

	hover        int
	hoverEnabled bool
	hunkLines    []bool // 按逻辑行记录是否跳过悬停（hunk 标题及分隔线）
}

func newDiffView() *diffView {
	return &diffView{
		TextView: tview.NewTextView().
			SetDynamicColors(true).
			SetScrollable(true).
			SetWrap(false),
		hover:        -1,
		hoverEnabled: true,
	}
}

// Draw 在 TextView 画完文本后叠加悬停行背景，保留每行原有的前景色和属性。
func (v *diffView) Draw(screen tcell.Screen) {
	v.TextView.Draw(screen)
	if v.hover < 0 {
		return
	}

	x, y, width, height := v.GetInnerRect()
	offset, _ := v.GetScrollOffset()
	row := y + v.hover - offset
	if row < y || row >= y+height {
		return
	}
	for column := x; column < x+width; column++ {
		mainc, combc, style, _ := screen.GetContent(column, row)
		screen.SetContent(column, row, mainc, combc, style.Background(colorSelection.cell))
	}
}

// MouseHandler 跟踪悬停行；只有行号变化时消费事件，让 tview 重绘一次。
func (v *diffView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	next := v.TextView.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		switch action {
		case tview.MouseMove:
			if v.hoverEnabled && v.hoverAt(event.Position()) {
				return true, nil
			}
		case tview.MouseScrollUp, tview.MouseScrollDown, tview.MouseScrollLeft, tview.MouseScrollRight:
			v.hover = -1
		}
		return next(action, event, setFocus)
	}
}

// SetHoverEnabled 控制鼠标悬停是否高亮整行。
func (v *diffView) SetHoverEnabled(enabled bool) {
	v.hoverEnabled = enabled
	if !enabled {
		v.hover = -1
	}
}

// diffHunkLinePattern 匹配悬停时需要跳过的行：hunk 标题、横线分隔，以及 delta 的 box 标题。
var diffHunkLinePattern = regexp.MustCompile(`^(?:@@|─|-\d+(?:,\d+)?\s+\+\d+(?:,\d+)?|\d+:.*│$)`)

// SetText 更新 diff 文本，并重新标记 hunk 标题；旧的高亮行不能沿用到新内容。
func (v *diffView) SetText(text string) *diffView {
	v.TextView.SetText(text)
	v.hover = -1
	lines := strings.Split(v.TextView.GetText(true), "\n")
	v.hunkLines = make([]bool, len(lines))
	for i, line := range lines {
		v.hunkLines[i] = diffHunkLinePattern.MatchString(strings.TrimSpace(line))
	}
	return v
}

// hoverAt 按鼠标位置更新悬停行；返回是否发生变化。
func (v *diffView) hoverAt(posX, posY int) bool {
	line := -1
	if v.InInnerRect(posX, posY) {
		_, innerY, _, _ := v.GetInnerRect()
		offset, _ := v.GetScrollOffset()
		if candidate := offset + posY - innerY; candidate >= 0 && candidate < len(v.hunkLines) && !v.hunkLines[candidate] {
			line = candidate
		}
	}
	if line == v.hover {
		return false
	}
	v.hover = line
	return true
}

// refTree 让滚轮滚动后的视口不被重绘拉回选中项。
// tview.TreeView 每次 Draw 都会把选中节点移进视口，跑马灯重绘时会把滚轮滚动的位置抹掉；
// 选中项在视口外时跳过这一步，滚动位置才能留住。
// 依赖 tview v0.42 的内部行为：SetCurrentNode 不触发 process，
// 且没有选中项时 process 不改 offsetY；升级 tview 时需要复查这两点。
type refTree struct {
	*tview.TreeView

	scrolled bool // 滚轮滚动过，选中项可以留在视口外
}

func newRefTree() *refTree {
	return &refTree{
		TreeView: tview.NewTreeView().
			SetTopLevel(1).
			SetGraphics(false),
	}
}

// SetCurrentNode 让显式选中的节点在下一次绘制时进入视口。
func (v *refTree) SetCurrentNode(node *tview.TreeNode) *refTree {
	v.scrolled = false
	v.TreeView.SetCurrentNode(node)
	return v
}

func (v *refTree) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	inner := v.TreeView.InputHandler()
	return func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		// 按键移动选中项后照常把它拉进视口。
		v.scrolled = false
		inner(event, setFocus)
	}
}

func (v *refTree) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
	inner := v.TreeView.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		if v.InRect(event.Position()) {
			switch action {
			case tview.MouseScrollUp, tview.MouseScrollDown:
				v.scrolled = true
			case tview.MouseLeftDown:
				v.scrolled = false
			}
		}
		return inner(action, event, setFocus)
	}
}

func (v *refTree) Draw(screen tcell.Screen) {
	node := v.TreeView.GetCurrentNode()
	if v.scrolled && v.offscreen(node) {
		// 选中项在视口外，绘制期间拿掉它，TreeView 就不会去改 offsetY。
		v.TreeView.SetCurrentNode(nil)
		v.TreeView.Draw(screen)
		v.TreeView.SetCurrentNode(node)
		return
	}
	v.TreeView.Draw(screen)
}

// offscreen 返回节点在树里但不在视口内。
// 节点被折叠隐藏时返回 false，让 TreeView 按原有行为回退到第一个可见项。
func (v *refTree) offscreen(node *tview.TreeNode) bool {
	row := v.row(node)
	if row < 0 {
		return false
	}
	_, _, _, height := v.GetInnerRect()
	offset := v.GetScrollOffset()
	return row < offset || row >= offset+height
}

// row 返回节点的可见行号；root 本身不显示，从它的子节点按层级 1 开始计数，找不到返回 -1。
func (v *refTree) row(node *tview.TreeNode) int {
	root := v.GetRoot()
	if root == nil || node == nil {
		return -1
	}
	row := -1
	var walk func(n *tview.TreeNode) bool
	walk = func(n *tview.TreeNode) bool {
		row++
		if n == node {
			return false
		}
		if !n.IsExpanded() {
			return true
		}
		for _, child := range n.GetChildren() {
			if !walk(child) {
				return false
			}
		}
		return true
	}
	for _, n := range root.GetChildren() {
		if !walk(n) {
			return row
		}
	}
	return -1
}

// rebaseForm 是 interactive rebase 弹窗，布局为说明、动作列表、提示和按钮三段。
// 动作状态和 rebase 规则由调用方维护，这里只负责展示和交互转发。
type rebaseForm struct {
	view         *tview.Flex
	list         *listView
	lines        []string // renderRebaseGraph 生成的、不含动作列的行
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
	onMoveCommit func(delta int),
	onCycleAction func(delta int),
	onCancel, onConfirm func(),
) *rebaseForm {
	f := &rebaseForm{lines: lines}

	frameStyle := tcell.StyleDefault.
		Foreground(tview.Styles.BorderColor).
		Background(colorPopup.cell)
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
		SetBorderStyle(frameStyle)
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
		case tcell.KeyRune:
			switch event.Rune() {
			case keymap.rebasePrev:
				onCycleAction(-1)
				return nil
			case keymap.rebaseNext:
				onCycleAction(1)
				return nil
			case keymap.rebaseMoveUp:
				onMoveCommit(-1)
				return nil
			case keymap.rebaseMoveDown:
				onMoveCommit(1)
				return nil
			}
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
		SetBorderStyle(frameStyle)

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

	// 弹窗铺满整个界面，左右各留 42 列、上下各留 8 行。
	view := tview.NewFlex().
		AddItem(nil, 42, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 42, 0, false)

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

// SetCommits 同时更新图线和动作列，用于移动 commit 后重绘列表。
func (f *rebaseForm) SetCommits(lines []string, actions []core.RebaseAction) {
	f.lines = lines
	f.SetActions(actions)
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

// rewordForm 是单条 commit reword 弹窗：说明、提交信息输入、提示和按钮。
type rewordForm struct {
	view         *tview.Flex
	subject      *tview.TextArea
	description  *tview.TextArea
	hint         *tview.TextView
	cancelButton *tview.Button
	rewordButton *tview.Button
}

func newRewordForm(
	commit core.Commit,
	selectedStyle tcell.Style,
	borderColor, focusColor tcell.Color,
	onCancel, onConfirm func(),
) *rewordForm {
	f := &rewordForm{}

	frameStyle := tcell.StyleDefault.
		Foreground(tview.Styles.BorderColor).
		Background(colorPopup.cell)
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

	message := fmt.Sprintf("Reword %s?\n\n%s",
		tview.Escape(commit.ShortHash()), tview.Escape(commit.Subject))
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetTextAlign(tview.AlignCenter).
		SetText(message)
	header.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1).
		SetBorderStyle(frameStyle)
	header.SetBackgroundColor(colorPopup.cell)

	newTextArea := func(title, text string, cursorAtTheEnd bool) *tview.TextArea {
		area := tview.NewTextArea().SetText(text, cursorAtTheEnd)
		area.SetTitle(title).
			SetTitleAlign(tview.AlignLeft).
			SetBorder(true).
			SetBorderPadding(0, 0, 1, 1).
			SetFocusFunc(func() { setFocusBorder(area.Box, true) }).
			SetBlurFunc(func() { setFocusBorder(area.Box, false) })
		area.SetBackgroundColor(colorPopup.cell)
		area.SetTextStyle(tcell.StyleDefault.
			Foreground(colorText.cell).
			Background(colorPopup.cell))
		area.SetSelectedStyle(selectedStyle)
		area.SetBorderStyle(borderStyle)
		return area
	}
	subject := newTextArea(" commit subject ", commit.Subject, true)
	// 多行 description 从开头显示；光标放在末尾会让文本框滚到最后一行，看起来像只取了一行。
	description := newTextArea(" description ", strings.Trim(commit.Body, "\r\n"), false)

	hint := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	hint.SetBackgroundColor(colorPopup.cell)
	hint.SetText(colorMuted.markup(tview.Escape("Local changes will be stashed and restored automatically.")))

	cancelButton := tview.NewButton("Cancel").
		SetSelectedFunc(onCancel)
	rewordButton := tview.NewButton("Reword").
		SetSelectedFunc(onConfirm)
	for _, button := range []*tview.Button{cancelButton, rewordButton} {
		button.SetStyle(buttonStyle).SetActivatedStyle(buttonActivatedStyle)
	}

	buttons := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(cancelButton, len("Cancel")+6, 0, false).
		AddItem(nil, 2, 0, false).
		AddItem(rewordButton, len("Reword")+6, 0, false).
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
		SetBorderStyle(frameStyle)

	parts := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 5, 0, false).
		AddItem(subject, 3, 0, false).
		AddItem(description, 0, 1, true).
		AddItem(footer, 5, 0, false)

	inner := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(nil, 15, 0, false).
		AddItem(parts, 0, 1, true).
		AddItem(nil, 15, 0, false)

	view := tview.NewFlex().
		AddItem(nil, 42, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 42, 0, false)

	f.view = view
	f.subject = subject
	f.description = description
	f.hint = hint
	f.cancelButton = cancelButton
	f.rewordButton = rewordButton

	setFocusBorder(subject.Box, true)
	setFocusBorder(description.Box, false)

	return f
}

// ResetHint 恢复默认的底部提示。
func (f *rewordForm) ResetHint() {
	f.hint.SetText(colorMuted.markup(tview.Escape("Local changes will be stashed and restored automatically.")))
}

// SetErrorHint 用危险色显示校验错误。
func (f *rewordForm) SetErrorHint(message string) {
	f.hint.SetText(colorDanger.markup(tview.Escape(message)))
}

// focusables 返回 Tab 循环的焦点顺序。
func (f *rewordForm) focusables() []tview.Primitive {
	return []tview.Primitive{f.subject, f.description, f.cancelButton, f.rewordButton}
}

// historyView 是文件历史弹窗：左侧 commit 列表，右侧文件 diff。
type historyView struct {
	view  *tview.Flex
	list  *listView
	diff  *diffView
	width int // diff 的渲染宽度
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
	list.SetHoverEnabled(false)
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

	diff := newDiffView()
	diff.SetText(colorMuted.markup("loading…"))

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
		case keymap.diffWhitespace:
			onToggleWhitespace()
			return nil
		case keymap.diffSideBySide:
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

	// 弹窗铺满 mainPage 所在区域，左右各留 18 列、上下各留 3 行，
	// 内容区正好是 mainPage 宽 - 36、高 - 6。
	view := tview.NewFlex().
		AddItem(nil, 18, 0, false).
		AddItem(inner, 0, 1, true).
		AddItem(nil, 18, 0, false)

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
