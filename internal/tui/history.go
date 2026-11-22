package tui

import "lanegraph/internal/core"

// historyPopup 是文件历史弹窗的业务状态，视图由 TUI 持有的 historyView 负责。
type historyPopup struct {
	change  core.FileChange
	commits []core.Commit
}

// setCommits 替换当前展示的历史提交。
func (p *historyPopup) setCommits(commits []core.Commit) {
	p.commits = commits
}

// commitAt 返回 index 对应的提交；越界时 ok 为 false。
func (p *historyPopup) commitAt(index int) (core.Commit, bool) {
	if index < 0 || index >= len(p.commits) {
		return core.Commit{}, false
	}
	return p.commits[index], true
}
