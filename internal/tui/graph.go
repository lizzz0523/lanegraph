package tui

import (
	"fmt"
	"strings"
	"unicode"

	"lanegraph/internal/core"

	"github.com/rivo/tview"
)

const (
	colorReset = "[-::-]" // 重置前景色、背景色和属性，否则 refs 的粗体会延续到 subject
	colorHash  = "[yellow]"
	colorRefs  = "[aqua::b]"
)

var laneColors = []string{
	"[green]",
	"[yellow]",
	"[blue]",
	"[fuchsia]",
	"[aqua]",
	"[red]",
}

func laneColor(lane int) string {
	return laneColors[lane%len(laneColors)]
}

// 作者色板与 lane 色板分开，避免作者的字母颜色和左侧 graph 线条混淆。
var authorColors = []string{
	"[#ff8700::b]",
	"[#5fd7ff::b]",
	"[#d787ff::b]",
	"[#5faf5f::b]",
	"[#ff87d7::b]",
	"[#ffd75f::b]",
	"[#87afff::b]",
	"[#d7af87::b]",
}

// authorColorMap 按 commit 列表中作者首次出现的顺序分配颜色，
// 保证同一作者在整个列表里颜色一致。
func authorColorMap(commits []core.Commit) map[string]string {
	colors := make(map[string]string, len(commits))
	for _, commit := range commits {
		if _, ok := colors[commit.Author]; ok {
			continue
		}
		colors[commit.Author] = authorColors[len(colors)%len(authorColors)]
	}
	return colors
}

func authorInitial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	return string(unicode.ToUpper([]rune(name)[0]))
}

type cell struct {
	up, down, left, right bool
	vColor, hColor        string
}

// glyphs 返回 cell 的线条字符和其右侧的连接字符，规则参考 lazygit：横线穿过竖线时竖线优先；
// 边汇入一条已存在的 lane 时用 ┤/├ 表示汇合点
func (c cell) glyphs() (line, conn string) {
	conn = " "
	if c.right {
		conn = "─"
	}
	switch {
	case c.up && c.down && c.left && !c.right:
		return "┤", conn
	case c.up && c.down && c.right && !c.left:
		return "├", conn
	case c.up && c.down:
		return "│", conn
	case c.up && c.left && c.right:
		return "┴", conn
	case c.up && c.left:
		return "╯", conn
	case c.up && c.right:
		return "╰", conn
	case c.up:
		return "╵", conn
	case c.down && c.left && c.right:
		return "┬", conn
	case c.down && c.left:
		return "╮", conn
	case c.down && c.right:
		return "╭", conn
	case c.down:
		return "╷", conn
	case c.left:
		return "─", conn
	case c.right:
		return "╶", conn
	default:
		return " ", conn
	}
}

func renderGraph(commits []core.Commit, graph core.Graph) []string {
	colorOf := authorColorMap(commits)

	lines := make([]string, 0, len(graph.Rows))
	for r, row := range graph.Rows {
		var out strings.Builder
		cells := make([]cell, graph.LaneCount)

		inRange := func(i int) bool {
			return i >= 0 && i < len(cells)
		}
		// 在节点 node 与 lane end 之间画一条边；isTop 表示边从上方进入，否则向下方离开
		drawEdge := func(end, node int, isTop bool) {
			color := laneColor(end)
			if inRange(end) {
				if isTop {
					cells[end].up = true
				} else {
					cells[end].down = true
				}
				cells[end].vColor = color
			}
			if end == node {
				return
			}
			lo, hi := min(end, node), max(end, node)
			for k := lo; k <= hi; k++ {
				if !inRange(k) {
					continue
				}
				if k > lo {
					cells[k].left = true
				}
				if k < hi {
					cells[k].right = true
				}
				cells[k].hColor = color
			}
		}

		for _, e := range row.Top {
			drawEdge(e[0], e[1], true)
		}
		for _, e := range row.Bottom {
			drawEdge(e[1], e[0], false)
		}

		commit := commits[r]
		// lane 数超过 maxLanes 时节点可能落在可视范围外，钳到最后一列保证每行都有节点
		nodeCol := min(row.NodeCol, len(cells)-1)
		for i, c := range cells {
			var char, conn string
			charColor := c.vColor
			if i == nodeCol {
				char = "◯"
				if len(commit.Parents) > 1 {
					char = "◎"
				}
				charColor = laneColor(i)
				conn = " "
				if c.right {
					conn = "─"
				}
			} else {
				char, conn = c.glyphs()
				if !c.up && !c.down {
					charColor = c.hColor
				}
			}
			fmt.Fprintf(&out, "%s%s%s%s%s", charColor, char, c.hColor, conn, colorReset)
		}

		hash := commit.Hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		fmt.Fprintf(&out, " %s%s%s", colorHash, hash, colorReset)
		fmt.Fprintf(&out, " %s%s%s", colorOf[commit.Author], tview.Escape(authorInitial(commit.Author)), colorReset)
		if commit.Refs != "" {
			fmt.Fprintf(&out, " %s(%s)%s", colorRefs, tview.Escape(commit.Refs), colorReset)
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.Subject))

		lines = append(lines, out.String())
	}

	return lines
}
