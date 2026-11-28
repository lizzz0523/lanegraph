package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// keymap 集中定义 TUI 中所有可打印字符快捷键。
// Ctrl 组合键、Esc、Tab、Enter 等 tcell.Key 不在其中，仍在各控件里直接判断。
var keymap = struct {
	quit rune
	help rune

	globalPull         rune
	globalPush         rune
	globalFetch        rune
	globalCreateBranch rune
	globalCreateTag    rune

	commitReset             rune
	commitRebase            rune
	commitInteractiveRebase rune
	commitReword            rune
	commitMerge             rune
	commitRevert            rune
	commitCherryPick        rune
	commitBranch            rune
	commitTag               rune
	commitParent            rune
	commitChild             rune
	commitInfo              rune

	fileOpen    rune
	fileHistory rune

	changeStage   rune
	changeDiscard rune

	refCheckout rune
	refDelete   rune

	diffWhitespace rune
	diffSideBySide rune

	listDown  rune
	listUp    rune
	listFirst rune
	listLast  rune

	rebasePrev     rune
	rebaseNext     rune
	rebaseMoveUp   rune
	rebaseMoveDown rune

	dialogClose rune
}{
	quit: 'q',
	help: '?',

	globalPull:         'p',
	globalPush:         'P',
	globalFetch:        'f',
	globalCreateBranch: 'B',
	globalCreateTag:    'T',

	commitReset:             'R',
	commitRebase:            'r',
	commitInteractiveRebase: 'i',
	commitReword:            'w',
	commitMerge:             'm',
	commitRevert:            'v',
	commitCherryPick:        'c',
	commitBranch:            'b',
	commitTag:               't',
	commitParent:            'n',
	commitChild:             'N',
	commitInfo:              ' ',

	fileOpen:    'o',
	fileHistory: 'h',

	changeStage:   ' ',
	changeDiscard: 'd',

	refCheckout: ' ',
	refDelete:   'd',

	diffWhitespace: 'w',
	diffSideBySide: 's',

	listDown:  'j',
	listUp:    'k',
	listFirst: 'g',
	listLast:  'G',

	rebasePrev:     'h',
	rebaseNext:     'l',
	rebaseMoveUp:   '[',
	rebaseMoveDown: ']',

	dialogClose: 'q',
}

// keyLabel 把 rune 转成帮助弹窗里展示的按键文本。
func keyLabel(key rune) string {
	if key == ' ' {
		return "Space"
	}
	return string(key)
}

// keyBinding 描述一个快捷键及其说明。
type keyBinding struct {
	keys string
	desc string
}

// keyBindings 按面板分组维护快捷键说明，同时用于帮助弹窗。
var keyBindings = []struct {
	name     string
	bindings []keyBinding
}{
	{"Global", []keyBinding{
		{keyLabel(keymap.help), "Show this help"},
		{keyLabel(keymap.quit) + " / Ctrl+Q", "Quit"},
		{keyLabel(keymap.globalPull), "Pull current branch"},
		{keyLabel(keymap.globalPush), "Push current branch"},
		{keyLabel(keymap.globalFetch), "Fetch remote branches and tags"},
		{keyLabel(keymap.globalCreateBranch), "Create and checkout branch from HEAD"},
		{keyLabel(keymap.globalCreateTag), "Create tag from HEAD"},
	}},
	{"Branchs & Tags", []keyBinding{
		{keyLabel(keymap.refCheckout), "Checkout branch or tag"},
		{keyLabel(keymap.refDelete), "Delete branch or tag"},
	}},
	{"All Commits", []keyBinding{
		{keyLabel(keymap.commitReset), "Reset current branch to selected commit"},
		{keyLabel(keymap.commitRebase), "Rebase current branch onto selected commit"},
		{keyLabel(keymap.commitInteractiveRebase), "Start interactive rebase"},
		{keyLabel(keymap.commitReword), "Reword selected commit"},
		{keyLabel(keymap.commitMerge), "Merge selected commit into current branch"},
		{keyLabel(keymap.commitRevert), "Revert selected commit"},
		{keyLabel(keymap.commitCherryPick), "Cherry-pick selected commit onto current branch"},
		{keyLabel(keymap.commitBranch), "Create and checkout branch from selected commit"},
		{keyLabel(keymap.commitTag), "Create tag from selected commit"},
		{keyLabel(keymap.commitParent), "Jump to first parent of selected commit"},
		{keyLabel(keymap.commitChild), "Jump to first child of selected commit"},
	}},
	{"Interative Rebase", []keyBinding{
		{"← / " + keyLabel(keymap.rebasePrev), "Previous rebase action"},
		{"→ / " + keyLabel(keymap.rebaseNext), "Next rebase action"},
		{keyLabel(keymap.rebaseMoveUp) + " / " + keyLabel(keymap.rebaseMoveDown), "Move selected commit up or down"},
	}},
	{"Commit Detail", []keyBinding{
		{keyLabel(keymap.commitInfo), "Show or close full commit detail"},
	}},
	{"Commit Files", []keyBinding{
		{keyLabel(keymap.fileOpen), "Open file in default app"},
		{keyLabel(keymap.fileHistory), "Show file history"},
	}},
	{"Local Changes", []keyBinding{
		{keyLabel(keymap.changeStage), "Stage or unstage file or folder"},
		{keyLabel(keymap.changeDiscard), "Discard changes"},
		{keyLabel(keymap.fileOpen), "Open file in default app"},
		{keyLabel(keymap.fileHistory), "Show file history"},
	}},
	{"File Diff", []keyBinding{
		{keyLabel(keymap.diffWhitespace), "Toggle whitespace"},
		{keyLabel(keymap.diffSideBySide), "Toggle side-by-side view"},
	}},
	{"Navigation", []keyBinding{
		{"↑ / ↓ or " + keyLabel(keymap.listDown) + " / " + keyLabel(keymap.listUp), "Move cursor"},
		{"PgUp / PgDn", "Page up or down"},
		{"Home / End or " + keyLabel(keymap.listFirst) + " / " + keyLabel(keymap.listLast), "First or last item"},
		{"Tab / Shift+Tab", "Switch focus"},
		{"Enter", "Confirm or expand folder"},
		{"Esc", "Close popup or leave input"},
		{keyLabel(keymap.dialogClose), "Close popup"},
	}},
}

// renderKeyBindings 生成帮助弹窗展示的快捷键列表。
func renderKeyBindings() string {
	width := 0
	for _, group := range keyBindings {
		for _, binding := range group.bindings {
			if w := runewidth.StringWidth(binding.keys); w > width {
				width = w
			}
		}
	}

	var text strings.Builder
	for i, group := range keyBindings {
		if i > 0 {
			text.WriteString("\n")
		}
		text.WriteString(colorAccent.markup(group.name, true))
		text.WriteString("\n")
		for _, binding := range group.bindings {
			text.WriteString("  ")
			text.WriteString(colorAccent.markup(binding.keys))
			text.WriteString(strings.Repeat(" ", width-runewidth.StringWidth(binding.keys)))
			text.WriteString("   ")
			text.WriteString(binding.desc)
			text.WriteString("\n")
		}
	}
	return text.String()
}
