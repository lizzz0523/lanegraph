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

	branches, err := gitBranches(repo)
	if err != nil {
		log.Fatal(err)
	}

	tui(branches, commits)
}

func tui(branches []branch, commits []commit) {
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical

	var app *tview.Application

	// 灰色底，替代默认选中态的白底
	listStyle := tcell.StyleDefault.
		Foreground(tview.Styles.PrimaryTextColor). // 与未选中行一致，不再反色
		Background(tcell.NewHexColor(0x4a4a4a))

	headerStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	headerSelectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorGray).
		Background(tcell.ColorNone).
		Bold(true)

	commitList := tview.NewList().
		SetSelectedStyle(listStyle).
		SetHighlightFullLine(true).
		ShowSecondaryText(false)

	commitList.SetTitle(" commits ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	// List 自带的滚轮只改视口偏移，且会被“保持选中项可见”的逻辑拉回，
	// 因此这里改为移动选中项。
	commitList.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		// capture 在 InRect 检查之前执行，这里自行限定范围，避免布局变化后吞掉其他面板的事件。
		if !commitList.InRect(event.Position()) {
			return action, event
		}
		switch action {
		case tview.MouseScrollUp:
			if cur := commitList.GetCurrentItem(); cur > 0 {
				commitList.SetCurrentItem(cur - 1)
			}
			return tview.MouseConsumed, nil
		case tview.MouseScrollDown:
			commitList.SetCurrentItem(commitList.GetCurrentItem() + 1)
			return tview.MouseConsumed, nil
		}
		return action, event
	})

	for _, line := range renderGraph(commits, computeGraph(commits)) {
		commitList.AddItem(line, "", 0, nil)
	}

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

	// commit hash -> graph 行号，用于点击分支后跳转
	rowOf := make(map[string]int, len(commits))
	for i, c := range commits {
		rowOf[c.hash] = i
	}

	for _, b := range branches {
		parent := localNode
		if b.remote {
			parent = remoteNode
		}

		child := tview.NewTreeNode(b.name).
			SetSelectedTextStyle(listStyle).
			SetSelectedFunc(func() {
				if row, ok := rowOf[b.hash]; ok {
					commitList.SetCurrentItem(row)
				}
				app.SetFocus(commitList)
			})

		parent.AddChild(child)
	}

	if len(remoteNode.GetChildren()) == 0 {
		branchRoot.RemoveChild(remoteNode)
	}

	branchList := tview.NewTreeView().
		SetRoot(branchRoot).
		SetTopLevel(1).
		SetGraphics(false)

	branchList.SetTitle(" branches ").
		SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	// 默认选中第一个分支，而不是分组标题
	if children := localNode.GetChildren(); len(children) > 0 {
		branchList.SetCurrentNode(children[0])
	} else if children := remoteNode.GetChildren(); len(children) > 0 {
		branchList.SetCurrentNode(children[0])
	}

	// TreeView 自带的滚轮只改视口偏移，选中项会被滚出可视区；
	// 因此这里改为移动选中项，与 commitList 保持一致。
	branchList.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		// tview 先执行 capture、后检查 InRect，且 Flex 会把事件依次交给每个子项；
		// 不判断范围，鼠标在 commitList 上滚动时事件也会被这里吞掉。
		if !branchList.InRect(event.Position()) {
			return action, event
		}
		switch action {
		case tview.MouseScrollUp:
			branchList.Move(-1)
			return tview.MouseConsumed, nil
		case tview.MouseScrollDown:
			branchList.Move(1)
			return tview.MouseConsumed, nil
		}
		return action, event
	})

	root := tview.NewFlex().
		AddItem(branchList, 40, 0, false).
		AddItem(commitList, 0, 1, true)

	app = tview.NewApplication().
		EnableMouse(true).
		SetRoot(root, true)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyCtrlQ || (event.Key() == tcell.KeyRune && event.Rune() == 'q'):
			app.Stop()
			return nil
		case event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab:
			// 只有两个面板，Tab 与 Shift-Tab 都是互相切换
			if commitList.HasFocus() {
				app.SetFocus(branchList)
			} else {
				app.SetFocus(commitList)
			}
			return nil
		}
		return event
	})

	if err := app.Run(); err != nil {
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
	cmd := exec.Command("git", "log", "--date-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%s")
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

type branch struct {
	name   string
	hash   string
	remote bool
}

func gitBranches(repo string) ([]branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	cmd := exec.Command("git", "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)",
		"refs/heads", "refs/remotes")
	cmd.Dir = repo

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var branches []branch
	for line := range strings.Lines(string(output)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		// %(symref) 非空表示符号引用（如 origin/HEAD）；
		// 它的 short name 是 origin，不跳过会多出一条假分支
		if len(parts) != 4 || parts[3] != "" {
			continue
		}
		branches = append(branches, branch{
			name:   parts[1],
			hash:   parts[2],
			remote: strings.HasPrefix(parts[0], "refs/remotes/"),
		})
	}
	return branches, nil
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
		if commit.refs != "" {
			fmt.Fprintf(&out, " %s(%s)%s", colorRefs, tview.Escape(commit.refs), colorReset)
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.subject))
		lines = append(lines, out.String())
	}

	return lines
}
