package tui

// keymap 集中定义 TUI 中所有可打印字符快捷键。
// Ctrl 组合键、Esc、Tab、Enter 等 tcell.Key 不在其中，仍在各控件里直接判断。
var keymap = struct {
	quit rune

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
	commitBranch            rune
	commitTag               rune
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

	rebasePrev rune
	rebaseNext rune

	dialogClose rune
}{
	quit: 'q',

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
	commitBranch:            'b',
	commitTag:               't',
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

	rebasePrev: 'h',
	rebaseNext: 'l',

	dialogClose: 'q',
}

// keyBinding 描述一个快捷键及其说明。
type keyBinding struct {
	key  rune
	desc string
}

// keyBindings 按面板分组维护 keymap 中按键的说明，供帮助面板等场景使用。
var keyBindings = []struct {
	name     string
	bindings []keyBinding
}{
	{"Global", []keyBinding{
		{keymap.quit, "Quit"},
		{keymap.globalPull, "Pull current branch"},
		{keymap.globalPush, "Push current branch"},
		{keymap.globalFetch, "Fetch remote refs"},
		{keymap.globalCreateBranch, "Create and checkout branch from HEAD"},
		{keymap.globalCreateTag, "Create tag from HEAD"},
	}},
	{"Commit", []keyBinding{
		{keymap.commitReset, "Reset current branch to selected commit"},
		{keymap.commitRebase, "Rebase current branch onto selected commit"},
		{keymap.commitInteractiveRebase, "Start interactive rebase"},
		{keymap.commitReword, "Reword selected commit"},
		{keymap.commitMerge, "Merge selected commit into current branch"},
		{keymap.commitBranch, "Create branch from selected commit"},
		{keymap.commitTag, "Create tag from selected commit"},
		{keymap.commitInfo, "Show full commit message"},
	}},
	{"Files", []keyBinding{
		{keymap.fileOpen, "Open file in default app"},
		{keymap.fileHistory, "Show file history"},
		{keymap.diffWhitespace, "Toggle whitespace"},
		{keymap.diffSideBySide, "Toggle side-by-side view"},
	}},
	{"Local changes", []keyBinding{
		{keymap.changeStage, "Stage or unstage file or folder"},
		{keymap.changeDiscard, "Discard changes"},
	}},
	{"Refs", []keyBinding{
		{keymap.refCheckout, "Checkout branch or tag"},
		{keymap.refDelete, "Delete ref"},
	}},
	{"Dialogs", []keyBinding{
		{keymap.dialogClose, "Close popup"},
		{keymap.listDown, "Next item"},
		{keymap.listUp, "Previous item"},
		{keymap.listFirst, "First item"},
		{keymap.listLast, "Last item"},
		{keymap.rebasePrev, "Previous rebase action"},
		{keymap.rebaseNext, "Next rebase action"},
	}},
}
