package main

import (
	"fmt"
	"sort"
	"strings"

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

// buildFileTree 按目录层级把文件变更组织成树。
func buildFileTree(changes []fileChange, selectedStyle tcell.Style) *tview.TreeNode {
	rootNode := tview.NewTreeNode("")

	folders := map[string]*tview.TreeNode{"": rootNode}
	for _, change := range changes {
		parts := strings.Split(change.path, "/")
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

		fileNode := tview.NewTreeNode(fileLabel(parts[len(parts)-1], change.status)).
			SetReference(change).
			SetSelectedTextStyle(selectedStyle)
		parent.AddChild(fileNode)
	}

	sortFoldersFirst(rootNode)
	return rootNode
}

// sortFoldersFirst 让同一层里的目录排在文件前面，组内保持原有顺序。
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
