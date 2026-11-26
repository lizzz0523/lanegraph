package tui

import (
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

// marqueeInterval 是跑马灯每滚动一格的间隔。
const marqueeInterval = 100 * time.Millisecond

// marqueeGap 是两份滚动文本之间隔开的空格数。
const marqueeGap = 3

// marquee 是一行文本的横向滚动状态。
// prefix 固定不动（可以带 markup，如图标），text 是需要滚动的纯文本。
type marquee struct {
	prefix string
	text   string
	offset int
}

// reset 替换滚动内容并回到起点。
func (m *marquee) reset(prefix, text string) {
	m.prefix = prefix
	m.text = text
	m.offset = 0
}

// fits 表示内容在 width 列内放不下，需要滚动。
func (m *marquee) fits(width int) bool {
	avail := width - tview.TaggedStringWidth(m.prefix)
	return avail > 0 && runewidth.StringWidth(m.text) > avail
}

// advance 前进一格并返回是否还有下一格；
// 走完一份 text 和间隔后回到起点，此时视觉上和初始画面一致。
func (m *marquee) advance() bool {
	if cycle := runewidth.StringWidth(m.text) + marqueeGap; m.offset+1 < cycle {
		m.offset++
		return true
	}
	m.offset = 0
	return false
}

// base 返回未滚动的完整文本。
func (m *marquee) base() string {
	return m.prefix + tview.Escape(m.text)
}

// frame 返回 width 列宽的当前帧文本。
func (m *marquee) frame(width int) string {
	avail := width - tview.TaggedStringWidth(m.prefix)
	if avail <= 0 || m.text == "" {
		return m.base()
	}
	source := m.text + strings.Repeat(" ", marqueeGap)
	return m.prefix + tview.Escape(marqueeWindow(source+source, m.offset, avail))
}

// marqueeWindow 从 text 的第 offset 格开始取 width 格；
// 宽字符在窗口边缘被切开时用空格补位。
func marqueeWindow(text string, offset, width int) string {
	var out strings.Builder
	col, written := 0, 0
	for _, r := range text {
		w := runewidth.RuneWidth(r)
		start, end := col, col+w
		col = end
		if end <= offset {
			continue
		}
		if start < offset {
			// offset 落在宽字符中间，左边补空格。
			pad := min(end-offset, width-written)
			out.WriteString(strings.Repeat(" ", pad))
			written += pad
		} else {
			if written+w > width {
				break
			}
			out.WriteRune(r)
			written += w
		}
		if written >= width {
			break
		}
	}
	if written < width {
		out.WriteString(strings.Repeat(" ", width-written))
	}
	return out.String()
}

// spinInterval 是点阵动画每切换一帧的间隔。
const spinInterval = 100 * time.Millisecond

// spinFrames 是点阵动画循环显示的帧序列。
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spin 是一组点阵字符循环切换的动画状态。
type spin struct {
	index int
}

// frame 返回当前帧字符。
func (s *spin) frame() string {
	return spinFrames[s.index]
}

// advance 前进一帧。
func (s *spin) advance() {
	s.index = (s.index + 1) % len(spinFrames)
}

// anim 按固定间隔在主循环里执行一帧。frame 返回 false 时自动停止。
type anim struct {
	stopCh chan struct{}
	once   sync.Once
}

// startAnim 启动逐帧动画；frame 每帧在主循环中调用，返回 false 表示结束。
func startAnim(app *tview.Application, interval time.Duration, frame func() bool) *anim {
	a := &anim{stopCh: make(chan struct{})}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-a.stopCh:
				return
			case <-ticker.C:
				app.QueueUpdateDraw(func() {
					select {
					case <-a.stopCh:
					default:
						if !frame() {
							a.stop()
						}
					}
				})
			}
		}
	}()

	return a
}

// stop 停止动画，可重复调用。
func (a *anim) stop() {
	a.once.Do(func() { close(a.stopCh) })
}
