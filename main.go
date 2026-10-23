package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func main() {
	repo, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 {
		repo = os.Args[1]
	}

	commits, err := gitCommits(repo)
	if err != nil {
		log.Fatal(err)
	}

	tui(renderGraph(commits, computeGraph(commits)))
}

func tui(lines []string) {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	app := tview.NewApplication()
	app.EnableMouse(true)

	listStyle := tcell.StyleDefault.
		Foreground(tview.Styles.PrimaryTextColor). // 与未选中行一致，不再反色
		Background(tcell.NewHexColor(0x3a3a3a))

	list := tview.NewList()
	list.SetTitle(" lanegraph ")
	list.SetBorder(true)
	list.SetBorderPadding(0, 0, 1, 1)
	list.SetSelectedStyle(listStyle) // 浅灰底，替代默认的白底
	list.SetHighlightFullLine(true)
	list.ShowSecondaryText(false)
	// Esc 退出
	list.SetDoneFunc(func() {
		app.Stop()
	})
	// List 自带的滚轮只改视口偏移，且会被“保持选中项可见”的逻辑拉回，
	// 因此这里改为移动选中项。
	list.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		switch action {
		case tview.MouseScrollUp:
			if cur := list.GetCurrentItem(); cur > 0 {
				list.SetCurrentItem(cur - 1)
			}
			return tview.MouseConsumed, nil
		case tview.MouseScrollDown:
			list.SetCurrentItem(list.GetCurrentItem() + 1)
			return tview.MouseConsumed, nil
		}
		return action, event
	})

	for _, line := range lines {
		list.AddItem(line, "", 0, nil)
	}

	if err := app.SetRoot(list, true).Run(); err != nil {
		log.Fatal(err)
	}
}

type commit struct {
	hash    string
	parents []string
	refs    string // %D，如 "HEAD -> main, origin/main, tag: v1"
	subject string
}

func gitCommits(repo string) ([]commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
	cmd := exec.Command("git", "log", "--date-order", "--branches", "--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%s")
	cmd.Dir = repo

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	var commits []commit
	for line := range strings.Lines(string(output)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, commit{
			hash:    parts[0],
			parents: strings.Fields(parts[1]),
			refs:    parts[2],
			subject: parts[3],
		})
	}
	return commits, nil
}

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
	colorReset = "[-]"
	colorHash  = "[yellow]"
	colorRefs  = "[cyan::b]"
)

var laneColors = []string{
	"[green]",
	"[yellow]",
	"[blue]",
	"[magenta]",
	"[cyan]",
	"[red]",
}

func laneColor(lane int) string {
	return laneColors[lane%len(laneColors)]
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
		for i, c := range cells {
			var char, conn string
			charColor := c.vColor
			if i == row.nodeCol {
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
		if commit.refs != "" {
			fmt.Fprintf(&out, " %s(%s)%s", colorRefs, tview.Escape(commit.refs), colorReset)
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.subject))
		lines = append(lines, out.String())
	}

	return lines
}
