package tui

import (
	"strings"
	"time"

	"github.com/lizzz0523/lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// refHoverDelay 是鼠标停在引用节点上多久后才开始跑马灯；移动经过的行不会触发。
const refHoverDelay = 300 * time.Millisecond

// refHover 是鼠标停在引用节点上、等待触发跑马灯的待定状态。
type refHover struct {
	node  *tview.TreeNode
	x, y  int
	timer *time.Timer
}

// refMarquee 是引用列表当前的跑马灯状态。
type refMarquee struct {
	marquee
	anim

	node  *tview.TreeNode
	x, y  int
	width int
}

// begin 绑定节点并开始滚动；内容在 level 层对应的宽度内放得下时返回 false。
func (m *refMarquee) begin(t *TUI, prefix, text string, bold bool, level int) bool {
	m.reset(prefix, text, bold)
	width := t.refMarqueeWidth(level)
	if !m.fits(width) {
		return false
	}
	m.width = width
	m.anim = anim{
		app:      t.app,
		interval: marqueeInterval,
		next:     func() bool { return m.step(t) },
		render:   func() { m.node.SetText(m.frame(m.width)) },
		restore:  func() { m.node.SetText(m.base()) },
	}
	m.start()
	return true
}

// step 重新确认鼠标仍在同一节点后前进一帧；目标失效或一轮播完时结束。
func (m *refMarquee) step(t *TUI) bool {
	node, level := t.refNodeAt(m.x, m.y)
	if node != m.node || t.popupVisible() {
		return m.drop(t)
	}
	width := t.refMarqueeWidth(level)
	if !m.fits(width) {
		return m.drop(t)
	}
	m.width = width
	// 一轮播完只停在原文，不解除会话：t.refMarquee 留作“这一行已经播过”的标记，
	// 这样指针在同一行内移动不会重播，离开该行时再由 stopRefMarquee 清除。
	return m.advance()
}

// drop 在目标失效时解除会话；anim 随后会把原文画回旧节点。
func (m *refMarquee) drop(t *TUI) bool {
	if t.refMarquee == m {
		t.refMarquee = nil
	}
	return false
}

// syncSpinner 把点阵动画绑定到 HEAD 分支前的标记。
type syncSpinner struct {
	spin
	anim
}

func (s *syncSpinner) init(t *TUI) {
	s.anim = anim{
		app:      t.app,
		interval: spinInterval,
		next: func() bool {
			s.advance()
			return true
		},
		render:  t.renderSyncIndicator,
		restore: t.renderSyncIndicator,
	}
}

// frame 返回当前应显示的动画帧；未播放时返回空串。
// 这里遮蔽了内嵌 spin 的 frame，取原始帧要显式走 s.spin.frame()。
func (s *syncSpinner) frame() string {
	if !s.playing() {
		return ""
	}
	return s.spin.frame()
}

// start 从头开始播放点阵动画。
func (s *syncSpinner) start() {
	if s.playing() {
		return
	}
	s.reset()
	s.anim.start()
}

// isBranchHead 表示 b 是否是 HEAD 指向的本地分支。
func isBranchHead(b core.Branch, headRef string) bool {
	return !b.Remote && b.Name == headRef
}

// branchPrefix 返回分支前面的标记和分隔空格；远端分支没有前缀。
func branchPrefix(b core.Branch, headRef, spinFrame string) string {
	switch {
	case isBranchHead(b, headRef):
		// HEAD 指向的本地分支前面加绿色对勾；同步操作期间换成点阵动画。
		mark := "✓"
		if spinFrame != "" {
			mark = spinFrame
		}
		return colorSuccess.markup(mark, true) + " "
	case !b.Remote:
		// 其余本地分支用分支符号占位对齐。
		return colorMuted.markup("↘") + " "
	}
	return ""
}

// branchLabel 返回分支在列表中的显示文本；spinFrame 非空时用动画帧代替 HEAD 对勾。
func branchLabel(b core.Branch, headRef, spinFrame string) string {
	name := tview.Escape(refLabel(b.LocalName()))
	if isBranchHead(b, headRef) {
		name = colorText.markup(name, true)
	}
	// remote 目录和分支前缀目录已经体现了路径，分支节点只显示最后一段。
	return branchPrefix(b, headRef, spinFrame) + name
}

// tagLabel 返回标签在列表中的显示文本。
func tagLabel(tag core.Tag) string {
	return tview.Escape(refLabel(tag.Name))
}

// refLabel 返回引用在树中显示的最后一段名字。
func refLabel(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// refLeaf 是 buildPrefixTree 的输入，统一分支和标签。
// group 是目录 key 的前缀；name 是完整引用名；label 是叶子显示文本；
// reference 是叶子的 reference（core.Branch 或 core.Tag）。
type refLeaf struct {
	group     string
	name      string
	label     string
	reference any
}

// buildPrefixTree 根据引用列表构建前缀目录树。
func buildPrefixTree(refs []refLeaf, selectedStyle tcell.Style) *tview.TreeNode {
	rootNode := tview.NewTreeNode("")

	folders := map[string]*tview.TreeNode{"": rootNode}
	for _, ref := range refs {
		parts := strings.Split(ref.name, "/")
		parent := rootNode
		prefix := ""

		for _, name := range parts[:len(parts)-1] {
			if prefix == "" {
				prefix = name
			} else {
				prefix += "/" + name
			}
			key := ref.group + "/" + prefix

			folderNode, ok := folders[key]
			if !ok {
				folder := folder{key: key, name: name}
				folderNode = tview.NewTreeNode(folderLabel(folder, true)).
					SetReference(folder).
					SetExpanded(true).
					SetSelectedTextStyle(selectedStyle)
				parent.AddChild(folderNode)

				folders[key] = folderNode
			}
			parent = folderNode
		}

		refNode := tview.NewTreeNode(ref.label).
			SetReference(ref.reference).
			SetSelectedTextStyle(selectedStyle)
		parent.AddChild(refNode)
	}

	sortFoldersFirst(rootNode)

	return rootNode
}

// buildRefTree 根据分支和标签列表构建引用树。
// remote 目录和 tags 目录默认折叠，refFolderExpanded 记录用户手动切换过的目录；
// 过滤激活时临时展开所有目录，让匹配的引用可见。
func buildRefTree(branches []core.Branch, tags []core.Tag, filter, headRef, spinFrame string, refFolderExpanded map[string]bool, headerStyle, headerSelectedStyle, selectedStyle tcell.Style) *tview.TreeNode {
	rootNode := tview.NewTreeNode("")

	// 隐藏 root，用 Local / Remote / Refs 作为分组标题。
	localNode := tview.NewTreeNode("Local").
		SetTextStyle(headerStyle).
		SetSelectedTextStyle(headerSelectedStyle)
	remoteNode := tview.NewTreeNode("Remote").
		SetTextStyle(headerStyle).
		SetSelectedTextStyle(headerSelectedStyle)
	refsNode := tview.NewTreeNode("Refs").
		SetTextStyle(headerStyle).
		SetSelectedTextStyle(headerSelectedStyle)

	// remote 名作为 Remote 下的目录节点，按分支原有顺序首次出现时创建。
	remoteFolders := make(map[string]*tview.TreeNode)
	var remoteNames []string
	remoteRefs := make(map[string][]refLeaf)
	var localRefs []refLeaf

	for _, branch := range branches {
		// 本地和远端分支都按完整分支名做大小写不敏感的 contains 过滤；
		// 远端分支的完整名带 remote 前缀（如 origin/foo）。
		if filter != "" && !strings.Contains(strings.ToLower(branch.Name), filter) {
			continue
		}

		ref := refLeaf{
			group:     "local",
			name:      branch.Name,
			label:     branchLabel(branch, headRef, spinFrame),
			reference: branch,
		}
		if !branch.Remote {
			localRefs = append(localRefs, ref)
			continue
		}

		// remote 目录已经体现了 remote 名，引用按去掉前缀后的名字建树。
		ref.name = branch.LocalName()
		if branch.RemoteName != "" {
			ref.group = "remote/" + branch.RemoteName
			folderNode, ok := remoteFolders[branch.RemoteName]
			if !ok {
				folder := folder{key: "remote/" + branch.RemoteName, name: branch.RemoteName}
				folderNode = tview.NewTreeNode(folderLabel(folder, filter != "")).
					SetReference(folder).
					SetExpanded(filter != "").
					SetSelectedTextStyle(selectedStyle)
				remoteNode.AddChild(folderNode)
				remoteFolders[branch.RemoteName] = folderNode
				remoteNames = append(remoteNames, branch.RemoteName)
			}
		}
		remoteRefs[branch.RemoteName] = append(remoteRefs[branch.RemoteName], ref)
	}

	for _, child := range buildPrefixTree(localRefs, selectedStyle).GetChildren() {
		localNode.AddChild(child)
	}

	for _, name := range remoteNames {
		for _, child := range buildPrefixTree(remoteRefs[name], selectedStyle).GetChildren() {
			remoteFolders[name].AddChild(child)
		}
	}
	// RemoteName 缺失时直接挂在 Remote 下。
	if refs := remoteRefs[""]; len(refs) > 0 {
		for _, child := range buildPrefixTree(refs, selectedStyle).GetChildren() {
			remoteNode.AddChild(child)
		}
	}

	// 标签放在 Refs 下的 tags 目录中，默认折叠。
	tagsFolder := folder{key: "refs/tags", name: "tags"}
	tagNode := tview.NewTreeNode(folderLabel(tagsFolder, filter != "")).
		SetReference(tagsFolder).
		SetExpanded(filter != "").
		SetSelectedTextStyle(selectedStyle)

	var tagRefs []refLeaf
	for _, tag := range tags {
		if filter != "" && !strings.Contains(strings.ToLower(tag.Name), filter) {
			continue
		}

		tagRefs = append(tagRefs, refLeaf{
			group:     "refs/tags",
			name:      tag.Name,
			label:     tagLabel(tag),
			reference: tag,
		})
	}
	for _, child := range buildPrefixTree(tagRefs, selectedStyle).GetChildren() {
		tagNode.AddChild(child)
	}

	if len(tagNode.GetChildren()) != 0 {
		refsNode.AddChild(tagNode)
	}

	for _, node := range []*tview.TreeNode{localNode, remoteNode, refsNode} {
		if len(node.GetChildren()) != 0 {
			rootNode.AddChild(node)
		}
	}

	// 目录先按默认状态展开/折叠，再恢复用户手动切换过的状态。
	if filter == "" {
		rootNode.Walk(func(node, _ *tview.TreeNode) bool {
			folder, ok := node.GetReference().(folder)
			if !ok {
				return true
			}
			if expanded, ok := refFolderExpanded[folder.key]; ok {
				setFolderExpanded(node, expanded)
			}
			return true
		})
	}

	return rootNode
}

// refName 返回节点对应引用的名字（本地/远端分支名或标签名）；
// 分组标题和目录返回空。
func refName(node *tview.TreeNode) string {
	switch ref := node.GetReference().(type) {
	case core.Branch:
		return ref.Name
	case core.Tag:
		return ref.Name
	}
	return ""
}

// refHash 返回节点对应提交的 hash；分组标题和目录返回空。
func refHash(node *tview.TreeNode) string {
	switch ref := node.GetReference().(type) {
	case core.Branch:
		return ref.Hash
	case core.Tag:
		return ref.Hash
	}
	return ""
}

// firstRefNode 返回树中第一个引用节点，跳过分组标题和目录。
func firstRefNode(root *tview.TreeNode) *tview.TreeNode {
	var first *tview.TreeNode
	root.Walk(func(node, _ *tview.TreeNode) bool {
		// Walk 的返回 false 只跳过子节点，不会终止遍历，所以命中后不要再覆盖。
		if first != nil {
			return false
		}
		if refName(node) != "" {
			first = node
		}
		return true
	})
	return first
}

// findRefNode 返回名字对应的引用节点，本地/远端分支和标签都会匹配；
// 名字为空或不存在时返回 nil。
func findRefNode(root *tview.TreeNode, name string) *tview.TreeNode {
	if root == nil || name == "" {
		return nil
	}
	var found *tview.TreeNode
	root.Walk(func(node, _ *tview.TreeNode) bool {
		if found != nil {
			return false
		}
		if refName(node) == name {
			found = node
		}
		return true
	})
	return found
}

// visibleRefNode 返回引用树中第 row 个可见节点及其层级。
// root 本身不显示（SetTopLevel(1)），所以从它的子节点开始按层级 1 计数；
// 行越界时返回 nil。
func visibleRefNode(root *tview.TreeNode, row int) (*tview.TreeNode, int) {
	var found *tview.TreeNode
	var level int

	var walk func(node *tview.TreeNode, depth int) bool
	walk = func(node *tview.TreeNode, depth int) bool {
		if row == 0 {
			found, level = node, depth
			return false
		}
		row--
		if !node.IsExpanded() {
			return true
		}
		for _, child := range node.GetChildren() {
			if !walk(child, depth+1) {
				return false
			}
		}
		return true
	}

	for _, node := range root.GetChildren() {
		if !walk(node, 1) {
			break
		}
	}
	return found, level
}
