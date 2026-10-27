package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"lanegraph/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var changeStatusColors = map[byte]string{
	'A': "[green]",
	'M': "[yellow]",
	'D': "[red]",
	'R': "[fuchsia]",
	'C': "[aqua]",
	'T': "[blue]",
}

func changeColor(status string) string {
	if status == "" {
		return "[gray]"
	}
	if color, ok := changeStatusColors[status[0]]; ok {
		return color
	}
	return "[gray]"
}

// changeLabel 只显示状态的首字母，避免 R100 这类相似度后缀占用空间。
func changeLabel(status string) string {
	if status == "" {
		return "?"
	}
	return status[:1]
}

func folderLabel(name string, expanded bool) string {
	if expanded {
		return "▼ " + tview.Escape(name)
	}
	return "▶ " + tview.Escape(name)
}

func fileLabel(name string, status string) string {
	return fmt.Sprintf("%s%s[-] %s", changeColor(status), changeLabel(status), tview.Escape(name))
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
				folderNode = tview.NewTreeNode(folderLabel(name, true)).
					SetReference(name).
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

	sortFoldersFirst(rootNode)
	return rootNode
}

var localChangeGroups = []struct {
	scopes []core.ChangeScope
	name   string
}{
	{[]core.ChangeScope{core.ScopeStaged}, "Staged Changes"},
	// 未暂存和未跟踪都属于“工作区里还没 add 的改动”，放在同一组。
	{[]core.ChangeScope{core.ScopeUnstaged, core.ScopeUntracked}, "Unstaged Changes"},
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

// sortFoldersFirst 使用稳定排序，保证同一层内的原有顺序不变。
func sortFoldersFirst(node *tview.TreeNode) {
	children := node.GetChildren()
	sort.SliceStable(children, func(i, j int) bool {
		iFolder := len(children[i].GetChildren()) > 0
		jFolder := len(children[j].GetChildren()) > 0
		return iFolder && !jFolder
	})
	node.SetChildren(children)

	for _, child := range children {
		sortFoldersFirst(child)
	}
}

func firstFileNode(node *tview.TreeNode) *tview.TreeNode {
	for _, child := range node.GetChildren() {
		if _, ok := child.GetReference().(core.FileChange); ok {
			return child
		}
		if found := firstFileNode(child); found != nil {
			return found
		}
	}
	return nil
}

// findLocalNode 在重建后的树里按来源和路径找回之前选中的文件。
func findLocalNode(node *tview.TreeNode, target core.FileChange) *tview.TreeNode {
	if target.Path == "" {
		return nil
	}
	for _, child := range node.GetChildren() {
		if change, ok := child.GetReference().(core.FileChange); ok {
			if change.Scope == target.Scope && change.Path == target.Path {
				return child
			}
			continue
		}
		if found := findLocalNode(child, target); found != nil {
			return found
		}
	}
	return nil
}

// loadDiff 统一处理 git-delta 的输出；delta 不可用时退回到原始 diff。
func loadDiff(output string, err error) (string, error) {
	switch {
	case err != nil && output == "":
		return "", err
	case err != nil:
		return buildPlainDiff(output), nil
	default:
		return buildDeltaDiff(output), nil
	}
}

func buildDeltaDiff(output string) string {
	output = strings.ReplaceAll(output, "\r", "")
	// delta 输出首尾可能带空行；末尾空行会在 TextView 中多出一行
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

// buildPlainDiff 是 git-delta 不可用时的兜底着色。
func buildPlainDiff(diff string) string {
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
		color := ""
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			color = "[gray]"
		case strings.HasPrefix(line, "+"):
			color = "[green]"
		case strings.HasPrefix(line, "-"):
			color = "[red]"
		case strings.HasPrefix(line, "@@"):
			color = "[aqua]"
		case strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file"),
			strings.HasPrefix(line, "deleted file"),
			strings.HasPrefix(line, "similarity"),
			strings.HasPrefix(line, "rename"),
			strings.HasPrefix(line, "Binary files"),
			strings.HasPrefix(line, "\\"):
			color = "[gray]"
		}
		if color != "" {
			out.WriteString(color)
		}
		out.WriteString(tview.Escape(line))
		if color != "" {
			out.WriteString("[-:-:-]")
		}
		out.WriteByte('\n')
	}
	return out.String()
}
