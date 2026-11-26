package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lizzz0523/lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var changeStatusColors = map[byte]themeColor{
	'A': colorSuccess,
	'M': colorSecondary,
	'D': colorDanger,
	'R': colorSpecial,
	'C': colorAccent,
	'T': colorInfo,
}

func changeColor(status string) themeColor {
	if status == "" {
		return colorMuted
	}
	if color, ok := changeStatusColors[status[0]]; ok {
		return color
	}
	return colorMuted
}

// changeLabel 只显示状态的首字母，避免 R100 这类相似度后缀占用空间。
func changeLabel(status string) string {
	if status == "" {
		return "?"
	}
	return status[:1]
}

func fileLabel(name string, status string) string {
	return fmt.Sprintf("%s %s", changeColor(status).markup(changeLabel(status)), tview.Escape(name))
}

func buildFileTree(changes []core.FileChange, selectedStyle tcell.Style) *tview.TreeNode {
	rootNode := tview.NewTreeNode("")

	folders := map[string]*tview.TreeNode{"": rootNode}
	for _, change := range changes {
		parts := strings.Split(change.Path, "/")
		parent := rootNode
		prefix := ""

		for _, name := range parts[:len(parts)-1] {
			if prefix == "" {
				prefix = name
			} else {
				prefix += "/" + name
			}

			folderNode, ok := folders[prefix]
			if !ok {
				folder := folder{key: name, name: name}
				folderNode = tview.NewTreeNode(folderLabel(folder, true)).
					SetReference(folder).
					SetExpanded(true).
					SetSelectedTextStyle(selectedStyle)
				parent.AddChild(folderNode)

				folders[prefix] = folderNode
			}
			parent = folderNode
		}

		fileNode := tview.NewTreeNode(fileLabel(parts[len(parts)-1], change.Status)).
			SetReference(change).
			SetSelectedTextStyle(selectedStyle)
		parent.AddChild(fileNode)
	}

	compactFolders(rootNode)
	sortFoldersFirst(rootNode)

	return rootNode
}

var localChangeGroups = []struct {
	scopes []core.ChangeScope
	name   string
}{
	// 未暂存和未跟踪都属于“工作区里还没 add 的改动”，放在同一组。
	{[]core.ChangeScope{core.ScopeUnstaged, core.ScopeUntracked}, "Unstaged Changes"},
	{[]core.ChangeScope{core.ScopeStaged}, "Staged Changes"},
}

// buildLocalTree 把本地变更分成暂存区和工作区两组，每组内部复用 buildFileTree 的目录树。
// 分组标题只作为 header 展示：不可选中，因此无法被折叠；即使该组没有文件也会显示，计数为 0。
func buildLocalTree(changes []core.FileChange, selectedStyle, headerStyle tcell.Style) *tview.TreeNode {
	rootNode := tview.NewTreeNode("")

	for _, group := range localChangeGroups {
		var files []core.FileChange
		for _, change := range changes {
			if slices.Contains(group.scopes, change.Scope) {
				files = append(files, change)
			}
		}

		title := fmt.Sprintf("%s (%d)", group.name, len(files))
		groupNode := tview.NewTreeNode(tview.Escape(title)).
			SetReference(group.name).
			SetTextStyle(headerStyle).
			SetSelectedTextStyle(headerStyle).
			SetExpanded(true).
			SetSelectable(false)
		for _, child := range buildFileTree(files, selectedStyle).GetChildren() {
			groupNode.AddChild(child)
		}
		rootNode.AddChild(groupNode)
	}

	return rootNode
}

func firstFileNode(root *tview.TreeNode) *tview.TreeNode {
	var first *tview.TreeNode
	root.Walk(func(node, _ *tview.TreeNode) bool {
		// Walk 的返回 false 只跳过子节点，不会终止遍历，所以命中后不要再覆盖。
		if first != nil {
			return false
		}
		if _, ok := node.GetReference().(core.FileChange); ok {
			first = node
		}
		return true
	})
	return first
}

// findLocalNode 在重建后的树里按来源和路径找回之前选中的文件。
// 文件在暂存区和未暂存区之间移动后来源会变化，因此再退一步只按路径匹配。
func findLocalNode(root *tview.TreeNode, target core.FileChange) *tview.TreeNode {
	if target.Path == "" {
		return nil
	}
	if found := findLocalNodeBy(root, target, true); found != nil {
		return found
	}
	return findLocalNodeBy(root, target, false)
}

func findLocalNodeBy(node *tview.TreeNode, target core.FileChange, matchScope bool) *tview.TreeNode {
	for _, child := range node.GetChildren() {
		if change, ok := child.GetReference().(core.FileChange); ok {
			if change.Path == target.Path && (!matchScope || change.Scope == target.Scope) {
				return child
			}
			continue
		}
		if found := findLocalNodeBy(child, target, matchScope); found != nil {
			return found
		}
	}
	return nil
}

// findLocalFolder 在重建后的树里按完整路径找回目录节点。
func findLocalFolder(root *tview.TreeNode, target string) *tview.TreeNode {
	if target == "" {
		return nil
	}
	for _, group := range root.GetChildren() {
		if found := findLocalFolderBy(group, "", target); found != nil {
			return found
		}
	}
	return nil
}

func findLocalFolderBy(node *tview.TreeNode, prefix, target string) *tview.TreeNode {
	for _, child := range node.GetChildren() {
		folder, ok := child.GetReference().(folder)
		if !ok {
			continue
		}
		full := folder.name
		if prefix != "" {
			full = prefix + "/" + folder.name
		}
		if full == target {
			return child
		}
		if found := findLocalFolderBy(child, full, target); found != nil {
			return found
		}
	}
	return nil
}

// folderChanges 收集目录节点下的全部文件变更。
func folderChanges(node *tview.TreeNode) []core.FileChange {
	var changes []core.FileChange
	for _, child := range node.GetChildren() {
		if change, ok := child.GetReference().(core.FileChange); ok {
			changes = append(changes, change)
			continue
		}
		changes = append(changes, folderChanges(child)...)
	}
	return changes
}

// renderDiff 统一处理着色器的输出；着色器都不可用时退回到原始 diff。
func renderDiff(output string, err error) (string, error) {
	switch {
	case err != nil && output == "":
		return "", err
	case err != nil:
		return renderPlainDiff(output), nil
	default:
		return renderANSIDiff(output), nil
	}
}

func renderANSIDiff(output string) string {
	output = strings.ReplaceAll(output, "\r", "")
	// 着色输出首尾可能带空行；末尾空行会在 TextView 中多出一行
	output = strings.Trim(output, "\n")

	var out strings.Builder
	out.Grow(len(output))

	var style ansiStyle
	pending := true
	for i := 0; i < len(output); {
		switch output[i] {
		case '\x1b':
			n := consumeEscape(output[i:], &style)
			if n <= 0 {
				n = 1
			}
			i += n
			pending = true
		case '\n':
			out.WriteByte('\n')
			i++
			// 每行重新输出一次当前样式，避免 tview 跨行保留的状态和这里不一致
			pending = true
		default:
			end := i
			for end < len(output) && output[end] != '\x1b' && output[end] != '\n' {
				end++
			}
			if pending {
				out.WriteString(style.tag())
				pending = false
			}
			out.WriteString(tview.Escape(output[i:end]))
			i = end
		}
	}
	return out.String()
}

// renderPlainDiff 是着色器都不可用时的兜底着色。
func renderPlainDiff(diff string) string {
	if diff == "" {
		return ""
	}
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	// 末尾换行会在 TextView 中多出一行
	diff = strings.TrimRight(diff, "\n")

	var out strings.Builder
	out.Grow(len(diff) + len(diff)/16)

	for line := range strings.Lines(diff) {
		line = strings.TrimRight(line, "\n")
		var color themeColor
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			color = colorMuted
		case strings.HasPrefix(line, "+"):
			color = colorSuccess
		case strings.HasPrefix(line, "-"):
			color = colorDanger
		case strings.HasPrefix(line, "@@"):
			color = colorAccent
		case strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file"),
			strings.HasPrefix(line, "deleted file"),
			strings.HasPrefix(line, "similarity"),
			strings.HasPrefix(line, "rename"),
			strings.HasPrefix(line, "Binary files"),
			strings.HasPrefix(line, "\\"):
			color = colorMuted
		}
		if color.tag != "" {
			out.WriteString(color.markup(tview.Escape(line)))
		} else {
			out.WriteString(tview.Escape(line))
		}
		out.WriteByte('\n')
	}
	return out.String()
}
