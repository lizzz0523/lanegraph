package tui

import (
	"strings"
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
	bold   bool
	offset int
}

// fits 表示内容在 width 列内放不下，需要滚动。
func (m *marquee) fits(width int) bool {
	avail := width - tview.TaggedStringWidth(m.prefix)
	return avail > 0 && runewidth.StringWidth(m.text) > avail
}

// base 返回未滚动的完整文本。
func (m *marquee) base() string {
	text := tview.Escape(m.text)
	if m.bold {
		text = colorText.markup(text, true)
	}
	return m.prefix + text
}

// reset 替换滚动内容并回到起点。
func (m *marquee) reset(prefix, text string, bold bool) {
	m.prefix = prefix
	m.text = text
	m.bold = bold
	m.offset = 0
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

// frame 返回 width 列宽的当前帧文本。
func (m *marquee) frame(width int) string {
	avail := width - tview.TaggedStringWidth(m.prefix)
	if avail <= 0 || m.text == "" {
		return m.base()
	}
	source := m.text + strings.Repeat(" ", marqueeGap)

	text := tview.Escape(marqueeWindow(source+source, m.offset, avail))
	if m.bold {
		text = colorText.markup(text, true)
	}
	return m.prefix + text
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

// reset 回到第一帧。
func (s *spin) reset() {
	s.index = 0
}

// advance 前进一帧。
func (s *spin) advance() {
	s.index = (s.index + 1) % len(spinFrames)
}

// frame 返回当前帧字符。
func (s *spin) frame() string {
	return spinFrames[s.index]
}

// anim 驱动一段逐帧动画：自己持有时钟，按 interval 调用 next 推进状态，
// 用 render / restore 分别画出当前帧和静止状态。每次 start 新建时钟，
// stop 可重复调用；一律以指针方式使用。
type anim struct {
	app      *tview.Application
	interval time.Duration
	next     func() bool
	render   func()
	restore  func()

	stopCh chan struct{}
}

// start 启动动画；已在运行时直接返回。
func (a *anim) start() {
	if a.stopCh != nil || a.app == nil {
		return
	}

	stopCh := make(chan struct{})
	a.stopCh = stopCh
	a.render()

	go func() {
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				a.app.QueueUpdateDraw(func() {
					select {
					case <-stopCh:
					default:
						if !a.next() {
							a.stop()
							return
						}
						a.render()
					}
				})
			}
		}
	}()
}

// stop 停止动画并恢复静止状态，可重复调用。
func (a *anim) stop() {
	if a.stopCh != nil {
		close(a.stopCh)
		a.stopCh = nil
	}
	a.restore()
}

// playing 表示动画是否正在运行。
func (a *anim) playing() bool {
	return a.stopCh != nil
}
