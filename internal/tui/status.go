package tui

import (
	"sort"
	"strings"

	"github.com/rivo/tview"
)

// statusStart 标记一个槽位正在加载；同一槽位重复调用只更新文案。
func (t *TUI) statusStart(slot, message string) {
	if t.statusBar == nil {
		return
	}
	if t.statusBusy == nil {
		t.statusBusy = make(map[string]string)
	}
	t.statusBusy[slot] = message
	t.renderStatus()
}

// statusDone 结束槽位的 loading；message 非空时同时更新最近完成信息。
func (t *TUI) statusDone(slot, message string) {
	if t.statusBar == nil {
		return
	}
	delete(t.statusBusy, slot)
	if message != "" {
		t.statusMsg = message
	}
	t.renderStatus()
}

func (t *TUI) renderStatus() {
	if len(t.statusBusy) == 0 {
		if t.statusMsg == "" {
			t.statusBar.SetText("")
			return
		}
		t.statusBar.SetText("[gray]" + tview.Escape(t.statusMsg) + "[-]")
		return
	}

	messages := make([]string, 0, len(t.statusBusy))
	for _, message := range t.statusBusy {
		messages = append(messages, tview.Escape(message))
	}
	sort.Strings(messages)
	t.statusBar.SetText("[gray]loading " + strings.Join(messages, ", ") + "…[-]")
}
