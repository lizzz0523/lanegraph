package tui

import (
	"strings"

	"lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// syncFrames 是 pull/push/fetch 期间代替 HEAD 对勾显示的点阵动画帧。
var syncFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// branchLabel 返回分支在列表中的显示文本；syncFrame >= 0 时用动画帧代替 HEAD 对勾。
func branchLabel(b core.Branch, headRef string, syncFrame int) string {
	// remote 目录已经体现了 remote 名，分支节点只显示后面的部分。
	label := tview.Escape(b.LocalName())
	switch {
	case !b.Remote && b.Name == headRef:
		// HEAD 指向的本地分支前面加绿色对勾；同步操作期间换成点阵动画。
		mark := "✓"
		if syncFrame >= 0 {
			mark = syncFrames[syncFrame%len(syncFrames)]
		}
		return "[green]" + mark + "[-] " + label
	case !b.Remote:
		// 其余本地分支用分支符号占位对齐。
		return "[gray]↘[-] " + label
	}
	return label
}

// remoteLabel 返回 Remote 分组节点的显示文本。
func remoteLabel(name string, expanded bool) string {
	if expanded {
		return "▼ " + tview.Escape(name)
	}
	return "▶ " + tview.Escape(name)
}

// buildBranchTree 根据分支列表和过滤条件构建分支树。
func buildBranchTree(branches []core.Branch, filter, headRef string, syncFrame int, headerStyle, headerSelectedStyle, selectedStyle tcell.Style) *tview.TreeNode {
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
					folderNode = tview.NewTreeNode(remoteLabel(branch.RemoteName, true)).
						SetReference(branch.RemoteName).
						SetExpanded(true).
						SetSelectedTextStyle(selectedStyle)
					remoteNode.AddChild(folderNode)
					remoteFolders[branch.RemoteName] = folderNode
				}
				parent = folderNode
			}
		}

		child := tview.NewTreeNode(branchLabel(branch, headRef, syncFrame)).
			SetReference(branch).
			SetSelectedTextStyle(selectedStyle)
		parent.AddChild(child)
	}

	if len(localNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(localNode)
	}
	if len(remoteNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(remoteNode)
	}

	return branchRoot
}

// firstBranchNode 返回树中第一个分支节点，跳过分组标题和 remote 目录。
func firstBranchNode(root *tview.TreeNode) *tview.TreeNode {
	var first *tview.TreeNode
	root.Walk(func(node, _ *tview.TreeNode) bool {
		// Walk 的返回 false 只跳过子节点，不会终止遍历，所以命中后不要再覆盖。
		if first != nil {
			return false
		}
		if _, ok := node.GetReference().(core.Branch); ok {
			first = node
		}
		return true
	})
	return first
}

// findBranchNode 返回分支 name 对应的节点，本地和远端都会匹配；name 为空或不存在时返回 nil。
func findBranchNode(root *tview.TreeNode, name string) *tview.TreeNode {
	if root == nil || name == "" {
		return nil
	}
	var found *tview.TreeNode
	root.Walk(func(node, _ *tview.TreeNode) bool {
		branch, ok := node.GetReference().(core.Branch)
		if ok && branch.Name == name {
			found = node
			return false
		}
		return true
	})
	return found
}

// setRemoteExpanded 同步 remote 分组节点的展开状态和箭头图标。
func setRemoteExpanded(node *tview.TreeNode, expanded bool) {
	node.SetExpanded(expanded)
	if name, ok := node.GetReference().(string); ok {
		node.SetText(remoteLabel(name, expanded))
	}
}
