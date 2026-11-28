package tui

import (
	"fmt"

	"github.com/lizzz0523/lanegraph/internal/core"
)

// rebaseActionOrder 是 ←/→ 切换时的动作循环顺序。
var rebaseActionOrder = []core.RebaseAction{
	core.RebasePick,
	core.RebaseSquash,
	core.RebaseFixup,
	core.RebaseDrop,
}

// rebasePopup 是 interactive rebase 的业务状态，视图由 TUI 持有的 rebaseForm 负责。
// 列表与 commit list 一样按新到旧显示，squash / fixup 会合入下方（更旧）的提交。
type rebasePopup struct {
	base    core.Commit
	commits []core.Commit
	actions []core.RebaseAction
}

// cycleAction 按 delta 在动作之间循环，只修改 row 对应的动作。
func (p *rebasePopup) cycleAction(row, delta int) {
	if row < 0 || row >= len(p.actions) {
		return
	}
	index := 0
	for i, action := range rebaseActionOrder {
		if action == p.actions[row] {
			index = i
			break
		}
	}
	index = (index + delta + len(rebaseActionOrder)) % len(rebaseActionOrder)
	p.actions[row] = rebaseActionOrder[index]
}

// move 把 row 对应的 commit 和动作一起移动到 row+delta，返回移动后的行号。
// 越界时保持原位并返回原行号。
func (p *rebasePopup) move(row, delta int) int {
	target := row + delta
	if row < 0 || row >= len(p.commits) || target < 0 || target >= len(p.commits) {
		return row
	}
	p.commits[row], p.commits[target] = p.commits[target], p.commits[row]
	p.actions[row], p.actions[target] = p.actions[target], p.actions[row]
	return target
}

// order 按 git todo 的顺序（旧到新）返回 commit hash；UI 列表是新到旧。
func (p *rebasePopup) order() []string {
	order := make([]string, len(p.commits))
	for i, commit := range p.commits {
		order[len(p.commits)-1-i] = commit.Hash
	}
	return order
}

// validateActions 校验动作并汇总为 full hash → action 的映射。
// 排除 drop 后最旧的提交必须可被 pick，否则它前面没有可供合并的提交。
func (p *rebasePopup) validateActions() (map[string]core.RebaseAction, error) {
	for i := len(p.actions) - 1; i >= 0; i-- {
		if p.actions[i] == core.RebaseDrop {
			continue
		}
		if p.actions[i] == core.RebaseSquash || p.actions[i] == core.RebaseFixup {
			return nil, fmt.Errorf("cannot %s %s without a previous commit", p.actions[i], p.commits[i].ShortHash())
		}
		break
	}

	actions := make(map[string]core.RebaseAction, len(p.actions))
	for i, action := range p.actions {
		actions[p.commits[i].Hash] = action
	}
	return actions, nil
}

// rebaseCommits 返回 base..HEAD 中会被 interactive rebase 重放的提交，
// 顺序与 commit 列表一致（新到旧）；默认 rebase 会丢弃 merge commit，这里同样过滤。
func rebaseCommits(commits []core.Commit, headHash, baseHash string) []core.Commit {
	byHash := make(map[string]core.Commit, len(commits))
	for _, commit := range commits {
		byHash[commit.Hash] = commit
	}

	reachable := reachableCommits(byHash, headHash)
	excluded := reachableCommits(byHash, baseHash)

	result := make([]core.Commit, 0, len(commits))
	for _, commit := range commits {
		if !reachable[commit.Hash] || excluded[commit.Hash] || len(commit.Parents) > 1 {
			continue
		}
		result = append(result, commit)
	}
	return result
}

// reachableCommits 返回从 start 出发沿 parent 可达的全部提交。
func reachableCommits(byHash map[string]core.Commit, start string) map[string]bool {
	seen := make(map[string]bool)
	stack := []string{start}
	for len(stack) > 0 {
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if hash == "" || seen[hash] {
			continue
		}
		seen[hash] = true
		if commit, ok := byHash[hash]; ok {
			stack = append(stack, commit.Parents...)
		}
	}
	return seen
}

// rewordPopup 是单条 commit reword 的业务状态。
type rewordPopup struct {
	commit core.Commit
}

// rewordValidation 返回不能 reword 的原因；空字符串表示可以执行。
func rewordValidation(commits []core.Commit, headHash string, commit core.Commit) string {
	if len(commit.Parents) > 1 {
		return "cannot reword a merge commit"
	}
	byHash := make(map[string]core.Commit, len(commits))
	for _, c := range commits {
		byHash[c.Hash] = c
	}
	if !reachableCommits(byHash, headHash)[commit.Hash] {
		return "commit is not on the current branch"
	}
	return ""
}

// renderRebaseGraph 按当前列表顺序渲染 rebase 预览。
// interactive rebase 的结果是线性的，因此把每个 commit 的 parent 临时改成下一个 commit，
// 避免非拓扑顺序让 ComputeGraph 把已经错过的 parent 画成旁支。
func renderRebaseGraph(commits []core.Commit) []string {
	ordered := make([]core.Commit, len(commits))
	copy(ordered, commits)
	for i := range ordered {
		if i+1 < len(ordered) {
			ordered[i].Parents = []string{ordered[i+1].Hash}
		} else {
			ordered[i].Parents = nil
		}
	}
	return renderCommitGraph(ordered, core.ComputeGraph(ordered), false)
}
