package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/lizzz0523/lanegraph/internal/core"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

var (
	colorHash = colorSecondary
	colorDate = colorMuted
	colorRefs = colorAccent
)

var laneColors = []themeColor{
	colorSuccess,
	colorSecondary,
	colorInfo,
	colorSpecial,
	colorAccent,
	colorDanger,
}

func laneColor(lane int) themeColor {
	return laneColors[lane%len(laneColors)]
}

// 作者色板与 lane 色板分开，避免作者的字母颜色和左侧 graph 线条混淆。
var authorColors = []themeColor{
	hexColor(0xff8700),
	hexColor(0x5fd7ff),
	hexColor(0xd787ff),
	hexColor(0x5faf5f),
	hexColor(0xff87d7),
	hexColor(0xffd75f),
	hexColor(0x87afff),
	hexColor(0xd7af87),
}

// authorColorMap 按 commit 列表中作者首次出现的顺序分配颜色，
// 保证同一作者在整个列表里颜色一致。
func authorColorMap(commits []core.Commit) map[string]themeColor {
	colors := make(map[string]themeColor, len(commits))
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

// commitDate 按 ls 的规则显示提交时间：半年内显示时分，否则显示年份。
func commitDate(date, now time.Time) string {
	if date.Before(now) && now.Sub(date) < 31556952*time.Second/2 {
		return date.Format("Jan _2 15:04")
	}
	return date.Format("Jan _2  2006")
}

// 线从格子上/下边缘进入后的走向
type course uint8

const (
	none course = iota
	straight
	toLeft
	toRight
)

type cell struct {
	up, down       course
	left, right    bool
	vColor, hColor themeColor
}

// kitty、Ghostty、Rex 会自己绘制私有区（PUA）U+F5D0–U+F60D 的 branch drawing symbols，
// 其中的节点字符带有通向格子边缘的线段，能和相邻的线条连在一起
var branchDrawing = os.Getenv("TERM_PROGRAM") == "ghostty" ||
	os.Getenv("TERM_PROGRAM") == "rex" ||
	os.Getenv("KITTY_WINDOW_ID") != ""

// glyphs 返回 cell 的线条字符和其右侧的连接字符，规则参考 lazygit：横线穿过竖线时竖线优先；
// 边汇入一条已存在的 lane 时用 ┤/├ 表示汇合点，汇合点同时被横线穿过时用 ┼。
// 终端支持 branch drawing symbols 时，弧线的组合改用能画出两段弧线、弧线叠横线的字符
func (c cell) glyphs() (line, conn string) {
	conn = " "
	if c.right {
		conn = "─"
	}
	if branchDrawing {
		switch {
		case c.up == toLeft && c.down == toLeft && c.right:
			return "\uf5e8", conn
		case c.up == toLeft && c.down == toLeft:
			return "\uf5df", conn
		case c.up == toRight && c.down == toRight && c.left:
			return "\uf5e9", conn
		case c.up == toRight && c.down == toRight:
			return "\uf5dc", conn
		case c.up == straight && c.down == toLeft && !c.right:
			return "\uf5de", conn
		case c.up == straight && c.down == toRight && !c.left:
			return "\uf5db", conn
		case c.up == toLeft && c.down == none && c.right:
			return "\uf5e3", conn
		case c.up == toRight && c.down == none && c.left:
			return "\uf5e4", conn
		case c.up == none && c.down == toLeft && c.right:
			return "\uf5e0", conn
		case c.up == none && c.down == toRight && c.left:
			return "\uf5e1", conn
		}
	}
	up, down := c.up != none, c.down != none
	joined := c.up > straight || c.down > straight
	switch {
	case up && down && c.left && c.right && joined:
		return "┼", conn
	case up && down && c.left && !c.right:
		return "┤", conn
	case up && down && c.right && !c.left:
		return "├", conn
	case up && down:
		return "│", conn
	case up && c.left && c.right:
		return "┴", conn
	case up && c.left:
		return "╯", conn
	case up && c.right:
		return "╰", conn
	case up:
		return "╵", conn
	case down && c.left && c.right:
		return "┬", conn
	case down && c.left:
		return "╮", conn
	case down && c.right:
		return "╭", conn
	case down:
		return "╷", conn
	case c.left:
		return "─", conn
	case c.right:
		return "╶", conn
	default:
		return " ", conn
	}
}

// nodeGlyph 返回节点字符；终端支持 branch drawing symbols 时节点会和 cell 中的线条相连，
// case 顺序与 glyphs 一致，码点参考 lazygit
func (c cell) nodeGlyph(merge bool) string {
	if !branchDrawing {
		if merge {
			return "◎"
		}
		return "◯"
	}
	up, down := c.up != none, c.down != none
	var r rune
	switch {
	case up && down && c.left && c.right:
		r = 0xf60c
	case up && down && c.left:
		r = 0xf606
	case up && down && c.right:
		r = 0xf604
	case up && down:
		r = 0xf5fa
	case up && c.left && c.right:
		r = 0xf60a
	case up && c.left:
		r = 0xf602
	case up && c.right:
		r = 0xf600
	case up:
		r = 0xf5f8
	case down && c.left && c.right:
		r = 0xf608
	case down && c.left:
		r = 0xf5fe
	case down && c.right:
		r = 0xf5fc
	case down:
		r = 0xf5f6
	case c.left && c.right:
		r = 0xf5f4
	case c.left:
		r = 0xf5f2
	case c.right:
		r = 0xf5f0
	default:
		r = 0xf5ee
	}
	// 上面是实心的 merge 节点，紧随其后的码点是同样连线的空心普通节点
	if !merge {
		r++
	}
	return string(r)
}

func renderGraph(commits []core.Commit, graph core.Graph, showRefs bool) []string {
	colorOf := authorColorMap(commits)
	now := time.Now()

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
				// 竖线在 lane end 处拐向节点所在的一侧
				line := straight
				if node < end {
					line = toLeft
				} else if node > end {
					line = toRight
				}
				if isTop {
					cells[end].up = line
				} else {
					cells[end].down = line
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
				char = c.nodeGlyph(len(commit.Parents) > 1)
				charColor = laneColor(i)
				conn = " "
				if c.right {
					conn = "─"
				}
			} else {
				char, conn = c.glyphs()
				if c.up == none && c.down == none {
					charColor = c.hColor
				}
			}
			fmt.Fprintf(&out, "%s%s", charColor.markup(char), c.hColor.markup(conn))
		}

		fmt.Fprintf(&out, " %s", colorHash.markup(commit.ShortHash()))
		fmt.Fprintf(&out, " %s", colorOf[commit.Author].markup(tview.Escape(authorInitial(commit.Author)), true))
		fmt.Fprintf(&out, " %s", colorDate.markup(tview.Escape(commitDate(commit.Date, now))))
		if showRefs && commit.Refs != "" {
			fmt.Fprintf(&out, " %s", colorRefs.markup("("+tview.Escape(commit.Refs)+")", true))
		}
		fmt.Fprintf(&out, " %s", tview.Escape(commit.Subject))

		lines = append(lines, out.String())
	}

	return lines
}

// renderCommit 生成 commit 信息面板的文本；width 用于截断正文首行。
func renderCommit(commit core.Commit, width int) string {
	if commit.Hash == "" {
		return ""
	}

	text := fmt.Sprintf("%s %s  %s",
		colorAccent.markup(tview.Escape(commit.Author)),
		colorMuted.markup(commit.Date.Format("Jan _2, 2006 15:04:05 -0700")),
		colorText.markup(tview.Escape(commit.Subject), true))
	if body := strings.TrimLeft(commit.Body, "\r\n"); body != "" {
		first, _, more := strings.Cut(body, "\n")
		if width > 4 {
			if truncate := runewidth.Truncate(first, width-4, ""); truncate != first {
				first, more = truncate, true
			}
		}
		text += "\n" + tview.Escape(first)
		if more {
			text += " " + colorMuted.markup("...")
		}
	}
	return text
}

// renderCommitFull 生成弹窗中的完整 commit 信息；作者/时间、subject 和 body 分段显示。
func renderCommitFull(commit core.Commit) string {
	if commit.Hash == "" {
		return ""
	}

	text := fmt.Sprintf("%s %s  %s",
		colorAccent.markup(tview.Escape(commit.Author)),
		colorMuted.markup(commit.Date.Format("Jan _2, 2006 15:04:05 -0700")),
		colorHash.markup(commit.Hash))
	text += "\n\n" + colorText.markup(tview.Escape(commit.Subject), true)
	if body := strings.Trim(commit.Body, "\r\n"); body != "" {
		text += "\n\n" + tview.Escape(body)
	}
	return text
}
