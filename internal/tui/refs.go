package tui

import (
	"strings"

	"github.com/lizzz0523/lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// refMarquee 是引用列表当前的跑马灯状态。
type refMarquee struct {
	marquee
	anim

	node  *tview.TreeNode
	x, y  int
	width int
}

// begin 绑定节点并开始滚动；内容在 level 层对应的宽度内放得下时返回 false。
func (m *refMarquee) begin(t *TUI, prefix, text string, level int) bool {
	m.reset(prefix, text)
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

// branchLabel 返回分支在列表中的显示文本；spinFrame 非空时用动画帧代替 HEAD 对勾。
func branchLabel(b core.Branch, headRef, spinFrame string) string {
	// remote 目录已经体现了 remote 名，分支节点只显示后面的部分。
	label := tview.Escape(b.LocalName())
	switch {
	case !b.Remote && b.Name == headRef:
		// HEAD 指向的本地分支前面加绿色对勾；同步操作期间换成点阵动画。
		mark := "✓"
		if spinFrame != "" {
			mark = spinFrame
		}
		return colorSuccess.markup(mark) + " " + label
	case !b.Remote:
		// 其余本地分支用分支符号占位对齐。
		return colorMuted.markup("↘") + " " + label
	}
	return label
}

// buildRefTree 根据分支和标签列表构建引用树。
// remote 目录和 tags 目录默认折叠，refFolderExpanded 记录用户手动展开过的目录；
// 过滤激活时临时展开所有目录，让匹配的引用可见。
func buildRefTree(branches []core.Branch, tags []core.Tag, filter, headRef, spinFrame string, refFolderExpanded map[string]bool, headerStyle, headerSelectedStyle, selectedStyle tcell.Style) *tview.TreeNode {
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
	refRoot := tview.NewTreeNode("").
		AddChild(localNode).
		AddChild(remoteNode).
		AddChild(refsNode)

	// remote 名作为 Remote 下的目录节点，按分支原有顺序首次出现时创建。
	remoteFolders := make(map[string]*tview.TreeNode)

	for _, branch := range branches {
		// 本地和远端分支都按完整分支名做大小写不敏感的 contains 过滤；
		// 远端分支的完整名带 remote 前缀（如 origin/foo）。
		if filter != "" && !strings.Contains(strings.ToLower(branch.Name), filter) {
			continue
		}

		parent := localNode
		if branch.Remote {
			parent = remoteNode
			if branch.RemoteName != "" {
				folderNode, ok := remoteFolders[branch.RemoteName]
				if !ok {
					folder := folder{key: "remote/" + branch.RemoteName, name: branch.RemoteName}
					expanded := filter != "" || refFolderExpanded[folder.key]
					folderNode = tview.NewTreeNode(folderLabel(folder, expanded)).
						SetReference(folder).
						SetExpanded(expanded).
						SetSelectedTextStyle(selectedStyle)
					remoteNode.AddChild(folderNode)
					remoteFolders[branch.RemoteName] = folderNode
				}
				parent = folderNode
			}
		}

		child := tview.NewTreeNode(branchLabel(branch, headRef, spinFrame)).
			SetReference(branch).
			SetSelectedTextStyle(selectedStyle)
		parent.AddChild(child)
	}

	// 标签放在 Refs 下的 tags 目录中，默认折叠。
	tagsFolder := folder{key: "tags", name: "tags"}
	tagsExpanded := filter != "" || refFolderExpanded[tagsFolder.key]
	tagNode := tview.NewTreeNode(folderLabel(tagsFolder, tagsExpanded)).
		SetReference(tagsFolder).
		SetExpanded(tagsExpanded).
		SetSelectedTextStyle(selectedStyle)
	refsNode.AddChild(tagNode)

	for _, tag := range tags {
		if filter != "" && !strings.Contains(strings.ToLower(tag.Name), filter) {
			continue
		}
		tagNode.AddChild(tview.NewTreeNode(tview.Escape(tag.Name)).
			SetReference(tag).
			SetSelectedTextStyle(selectedStyle))
	}

	if len(tagNode.GetChildren()) == 0 {
		refsNode.RemoveChild(tagNode)
	}

	for _, node := range []*tview.TreeNode{localNode, remoteNode, refsNode} {
		if len(node.GetChildren()) == 0 {
			refRoot.RemoveChild(node)
		}
	}

	return refRoot
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
