package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/rivo/tview"
)

type graphRow struct {
	nodeCol int
	top     [][2]int
	bottom  [][2]int
}

type graph struct {
	rows      []graphRow
	laneCount int
}

const MAX_LANES = 24

func computeGraph(commits []commit) graph {
	rows := make([]graphRow, 0, len(commits))
	lanes := []string{}
	laneCount := 1

	for _, commit := range commits {
		expected := []int{}
		for i := range lanes {
			if lanes[i] == commit.hash {
				expected = append(expected, i)
			}
		}

		var nodeCol int
		if len(expected) > 0 {
			nodeCol = expected[0]
		} else {
			nodeCol = freeSlot(lanes)
		}

		top := [][2]int{}
		for i := range lanes {
			if lanes[i] == "" {
				continue
			}
			if slices.Contains(expected, i) {
				top = append(top, [2]int{i, nodeCol})
			} else {
				top = append(top, [2]int{i, i})
			}
		}

		for _, i := range expected {
			lanes[i] = ""
		}
		if nodeCol < len(lanes) {
			lanes[nodeCol] = ""
		}

		parentCols := []int{}
		for i, parent := range commit.parents {
			// 第一个 parent 始终沿用节点所在 lane，保持主线连续；
			// 若该 parent 已在其他 lane 中，会在它自己那一行汇合
			parentCol := nodeCol
			if i > 0 {
				parentCol = slices.IndexFunc(lanes, func(lane string) bool { return lane == parent })
				if parentCol == -1 {
					parentCol = freeSlot(lanes)
				}
			}
			lanes = ensureLen(lanes, parentCol)
			lanes[parentCol] = parent
			if !slices.Contains(parentCols, parentCol) {
				parentCols = append(parentCols, parentCol)
			}
		}

		bottom := [][2]int{}
		for i := range lanes {
			if lanes[i] == "" {
				continue
			}
			if slices.Contains(parentCols, i) {
				bottom = append(bottom, [2]int{nodeCol, i})
			} else {
				bottom = append(bottom, [2]int{i, i})
			}
		}

		for len(lanes) > 0 && lanes[len(lanes)-1] == "" {
			lanes = lanes[:len(lanes)-1]
		}

		for _, edge := range slices.Concat(top, bottom) {
			laneCount = max(laneCount, edge[0]+1, edge[1]+1)
		}
		laneCount = max(laneCount, nodeCol+1, len(lanes))

		rows = append(rows, graphRow{nodeCol: nodeCol, top: top, bottom: bottom})
	}

	return graph{rows: rows, laneCount: min(MAX_LANES, max(1, laneCount))}
}

func freeSlot(lanes []string) int {
	slot := slices.IndexFunc(lanes, func(lane string) bool { return lane == "" })
	if slot != -1 {
		return slot
	}
	return len(lanes)
}

func ensureLen(lanes []string, idx int) []string {
	for idx >= len(lanes) {
		lanes = append(lanes, "")
	}
	return lanes
}

const (
	colorReset = "[-::-]" // 同时重置前景色和属性，否则 refs 的粗体会延续到 subject
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
func authorColorMap(commits []commit) map[string]string {
	colors := make(map[string]string, len(commits))
	for _, commit := range commits {
		if _, ok := colors[commit.author]; ok {
			continue
		}
		colors[commit.author] = authorColors[len(colors)%len(authorColors)]
	}
	return colors
}

// authorInitial 返回作者名的首字符并转为大写，空名回退为 "?"。
func authorInitial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	return string(unicode.ToUpper([]rune(name)[0]))
}

type cell struct {
	up, down, left, right bool
	vColor, hColor        string // 竖线颜色 / 横线颜色
}

// glyphs 返回 cell 的线条字符和其右侧的连接字符，规则基本与 lazygit 一致：横线穿过竖线时竖线优先；
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

func renderGraph(commits []commit, graph graph) []string {
	colorOf := authorColorMap(commits)

	lines := make([]string, 0, len(graph.rows))
	for r, row := range graph.rows {
		var out strings.Builder
		cells := make([]cell, graph.laneCount)

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

		for _, e := range row.top {
			drawEdge(e[0], e[1], true)
		}
		for _, e := range row.bottom {
			drawEdge(e[1], e[0], false)
		}

		commit := commits[r]
		// lane 数超过 MAX_LANES 时节点可能落在可视范围外，钳到最后一列保证每行都有节点
		nodeCol := min(row.nodeCol, len(cells)-1)
		for i, c := range cells {
			var char, conn string
			charColor := c.vColor
			if i == nodeCol {
				char = "◯"
				if len(commit.parents) > 1 {
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

		hash := commit.hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		fmt.Fprintf(&out, " %s%s%s", colorHash, hash, colorReset)
		fmt.Fprintf(&out, " %s%s%s", colorOf[commit.author], tview.Escape(authorInitial(commit.author)), colorReset)
		if commit.refs != "" {
			fmt.Fprintf(&out, " %s(%s)%s", colorRefs, tview.Escape(commit.refs), colorReset)
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.subject))

		lines = append(lines, out.String())
	}

	return lines
}
