package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitRoot 返回仓库根目录；repo 不在 git 仓库内时返回原路径。
// 路径类 git 命令都以仓库根目录为基准，从子目录启动时也要统一到根目录。
func GitRoot(ctx context.Context, repo string) string {
	root, err := gitOutputTrimmed(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return repo
	}
	return root
}

// GitRepoHead 返回 HEAD 的显示名和 hash。
// HEAD 在分支上时用分支名；detached 时优先用指向该提交的 tag，没有 tag 则用短 hash。
func GitRepoHead(ctx context.Context, repo string) (string, string) {
	hash, _ := gitOutputTrimmed(ctx, repo, "rev-parse", "HEAD")

	// HEAD 指向分支时直接显示分支名
	if ref, err := gitOutputTrimmed(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && ref != "" {
		return ref, hash
	}
	// detached HEAD：优先显示指向该提交的 tag。
	// for-each-ref --points-at 不会解引用 annotated tag，改用 git tag 才能匹配。
	// 这里不能回退到指向同一提交的本地分支，否则 UI 会把它当成已检出的分支。
	if tags, err := gitOutputTrimmed(ctx, repo, "tag", "--points-at", "HEAD"); err == nil && tags != "" {
		if ref, _, _ := strings.Cut(tags, "\n"); ref != "" {
			return ref, hash
		}
	}
	// 没有 tag 可显示时回退到 7 位短 hash
	if hash != "" {
		return shortHash(hash), hash
	}
	return "HEAD", ""
}

type Branch struct {
	Name   string
	Hash   string
	Remote bool
	// RemoteName 是远端分支所属的 remote（如 origin）；本地分支为空。
	// 用于把 origin/foo 这类远端分支映射到同名本地分支 foo。
	RemoteName string
}

// LocalName 返回分支对应的本地分支名：本地分支返回自身，
// 远端分支去掉 remote 前缀（如 origin/foo → foo）。
func (b Branch) LocalName() string {
	if !b.Remote {
		return b.Name
	}
	if b.RemoteName != "" && strings.HasPrefix(b.Name, b.RemoteName+"/") {
		return strings.TrimPrefix(b.Name, b.RemoteName+"/")
	}
	// RemoteName 缺失时退化为去掉第一段。
	if _, rest, ok := strings.Cut(b.Name, "/"); ok {
		return rest
	}
	return b.Name
}

func GitBranches(ctx context.Context, repo string) ([]Branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	refs, err := gitOutputTrimmed(ctx, repo, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var branches []Branch
	for line := range strings.Lines(refs) {
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

		branch := Branch{
			Name:   parts[1],
			Hash:   parts[2],
			Remote: strings.HasPrefix(parts[0], "refs/remotes/"),
		}
		if branch.Remote {
			// 用完整 refname 解析 remote，分支名里带 / 时也能正确去掉前缀
			if remote, _, ok := strings.Cut(strings.TrimPrefix(parts[0], "refs/remotes/"), "/"); ok {
				branch.RemoteName = remote
			}
		}
		branches = append(branches, branch)
	}
	return branches, nil
}

// GitCreateBranch 从 startPoint 创建并检出分支；startPoint 为空时从 HEAD 创建。
// 返回新建的分支名。
func GitCreateBranch(ctx context.Context, repo, name, startPoint string) (string, error) {
	args := []string{"checkout", "-b", name}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return "", fmt.Errorf("git checkout error: %w", gitStderr(err))
	}
	return name, nil
}

// GitDeleteBranch 删除本地分支；force 为 true 时用 -D 删除未合并的分支。
func GitDeleteBranch(ctx context.Context, repo, name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := gitOutput(ctx, repo, nil, "branch", flag, name); err != nil {
		return fmt.Errorf("git branch delete error: %w", gitStderr(err))
	}
	return nil
}

// GitDeleteRemoteBranch 在远端删除 branch 对应的分支。
func GitDeleteRemoteBranch(ctx context.Context, repo string, branch Branch) error {
	if branch.RemoteName == "" {
		return fmt.Errorf("unknown remote for branch: %s", branch.Name)
	}
	if _, err := gitOutput(ctx, repo, gitRemoteEnv(), "push", branch.RemoteName, "--delete", branch.LocalName()); err != nil {
		return fmt.Errorf("git push error: %w", gitStderr(err))
	}
	return nil
}

// GitCheckoutBranch 切换到 branch 对应的本地分支。
// 本地分支直接 checkout；远端分支切换到同名本地分支，
// 本地分支不存在时从该远端分支创建跟踪分支。返回实际检出的本地分支名。
func GitCheckoutBranch(ctx context.Context, repo string, branch Branch) (string, error) {
	local := branch.LocalName()

	// 远端分支对应的本地分支已存在时直接切换，不重置、不改 upstream。
	if branch.Remote && !gitRefExists(ctx, repo, "refs/heads/"+local) {
		if _, err := gitOutput(ctx, repo, nil, "checkout", "-b", local, "--track", branch.Name); err != nil {
			return "", fmt.Errorf("git checkout error: %w", gitStderr(err))
		}
		return local, nil
	}

	return GitCheckout(ctx, repo, local)
}

// Tag 是仓库中的一个标签，Hash 是它指向的对象（通常是提交）。
type Tag struct {
	Name string
	Hash string
}

func GitTags(ctx context.Context, repo string) ([]Tag, error) {
	// 按 creatordate 倒序，新打的标签排在前面；
	// annotated tag 用 tag 创建时间，轻量标签回退到提交时间。
	refs, err := gitOutputTrimmed(ctx, repo, "for-each-ref", "--sort=-creatordate",
		"--format=%(refname:short)%00%(objectname)%00%(*objectname)",
		"refs/tags")
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var tags []Tag
	for line := range strings.Lines(refs) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 {
			continue
		}
		// annotated tag 的 objectname 是 tag 对象本身，*objectname 才是它指向的对象；
		// 轻量标签没有 tag 对象，*objectname 为空。
		hash := parts[1]
		if parts[2] != "" {
			hash = parts[2]
		}
		tags = append(tags, Tag{Name: parts[0], Hash: hash})
	}
	return tags, nil
}

// GitCreateTag 在 startPoint 创建轻量标签；startPoint 为空时从 HEAD 创建。
// 返回新建的标签名。
func GitCreateTag(ctx context.Context, repo, name, startPoint string) (string, error) {
	args := []string{"tag", name}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return "", fmt.Errorf("git tag error: %w", gitStderr(err))
	}
	return name, nil
}

// GitDeleteTag 删除标签。
func GitDeleteTag(ctx context.Context, repo, name string) error {
	if _, err := gitOutput(ctx, repo, nil, "tag", "-d", name); err != nil {
		return fmt.Errorf("git tag delete error: %w", gitStderr(err))
	}
	return nil
}

// GitCheckout 检出 ref；标签、提交或远端分支会让 HEAD 进入 detached 状态。返回 ref 名。
func GitCheckout(ctx context.Context, repo, ref string) (string, error) {
	if _, err := gitOutput(ctx, repo, nil, "checkout", ref); err != nil {
		return "", fmt.Errorf("git checkout error: %w", gitStderr(err))
	}
	return ref, nil
}

type Commit struct {
	Hash    string
	Parents []string
	Refs    string // %D，如 "HEAD -> main, origin/main, tag: v1"
	Author  string
	Date    time.Time
	Subject string
	Body    string
}

// shortHash 返回 7 位短 hash。
func shortHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// ShortHash 返回提交的 7 位短 hash。
func (c Commit) ShortHash() string {
	return shortHash(c.Hash)
}

// diffBase 返回 diff 的基准提交：优先使用显式 compare，其次第一个 parent。
// 根提交没有 parent 且没有 compare 时返回空，由调用方退回 git show。
func (c Commit) diffBase(compare *Commit) string {
	if compare != nil {
		return compare.Hash
	}
	if len(c.Parents) > 0 {
		return c.Parents[0]
	}
	return ""
}

func GitCommits(ctx context.Context, repo string) ([]Commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签，未合并的分支也会被画出来
	// topo-order 让同一分支的提交连续输出，减少 lane 交错；它和 date-order 一样保证父提交在子提交之后
	// -z 用 NUL 分隔记录，body 里的换行才不会把一条记录拆成多行
	log, err := gitOutputTrimmed(ctx, repo, "log", "-z", "--topo-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%an%x00%cI%x00%s%x00%b")
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	// 字段和记录都以 NUL 分隔，每 7 段是一个 commit；末尾终止符会多出一个空段
	parts := strings.Split(log, "\x00")
	var commits []Commit
	for i := 0; i+7 <= len(parts); i += 7 {
		if parts[i] == "" {
			continue
		}
		date := time.Unix(0, 0)
		if parsed, err := time.Parse(time.RFC3339, parts[i+4]); err == nil {
			date = parsed
		}
		commits = append(commits, Commit{
			Hash:    parts[i],
			Parents: strings.Fields(parts[i+1]),
			Refs:    parts[i+2],
			Author:  parts[i+3],
			Date:    date,
			Subject: strings.TrimRight(parts[i+5], "\r\n"),
			Body:    strings.ReplaceAll(strings.TrimRight(parts[i+6], "\r\n"), "\r\n", "\n"),
		})
	}
	return commits, nil
}

// GitFileHistory 返回 path 涉及的提交历史，按 git log 默认顺序（新到旧）。
func GitFileHistory(ctx context.Context, repo, path string) ([]Commit, error) {
	log, err := gitOutputTrimmed(ctx, repo, "log", "-z", "--format=%H%x00%P%x00%an%x00%cI%x00%s", "--", path)
	if err != nil {
		return nil, fmt.Errorf("git file history error: %w", err)
	}

	// 字段和记录都以 NUL 分隔，每 5 段是一个 commit。
	parts := strings.Split(log, "\x00")
	var commits []Commit
	for i := 0; i+5 <= len(parts); i += 5 {
		if parts[i] == "" {
			continue
		}
		date := time.Unix(0, 0)
		if parsed, err := time.Parse(time.RFC3339, parts[i+3]); err == nil {
			date = parsed
		}
		commits = append(commits, Commit{
			Hash:    parts[i],
			Parents: strings.Fields(parts[i+1]),
			Author:  parts[i+2],
			Date:    date,
			Subject: strings.TrimRight(parts[i+4], "\r\n"),
		})
	}
	return commits, nil
}

// GitPull 拉取当前分支的上游变更，返回 git 的输出。
func GitPull(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitRemoteEnv(), "pull")
	if err != nil {
		return "", fmt.Errorf("git pull error: %w", gitStderr(err))
	}
	return output, nil
}

// ErrNonFastForward 表示 push 因非快进被拒绝：远端含有本地缺失的提交，
// 常见于 rebase / amend 之后，需要 force push。
var ErrNonFastForward = errors.New("push rejected: non-fast-forward")

// GitPush 推送当前分支到它的上游远端，返回 git 的输出。
// force 为 true 时使用 --force-with-lease，避免覆盖别人刚推上去的提交。
func GitPush(ctx context.Context, repo string, force bool) (string, error) {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	output, err := gitOutput(ctx, repo, gitRemoteEnv(), args...)
	if err != nil {
		err = gitStderr(err)
		// 只有非快进才提示 force push；本地落后（fetch first）时强制推送会丢掉远端提交。
		if strings.Contains(err.Error(), "(non-fast-forward)") {
			return "", ErrNonFastForward
		}
		return "", fmt.Errorf("git push error: %w", err)
	}
	return output, nil
}

// GitFetch 拉取远端引用，返回 git 的输出。
func GitFetch(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitRemoteEnv(), "fetch")
	if err != nil {
		return "", fmt.Errorf("git fetch error: %w", gitStderr(err))
	}
	return output, nil
}

// gitRemoteEnv 返回访问远端时使用的非交互环境：
// TUI 独占终端，禁止 git 弹出凭据和编辑器提示。
func gitRemoteEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
}

// ResetMode 是 git reset 的重置模式。
type ResetMode uint8

const (
	ResetSoft ResetMode = iota
	ResetMixed
	ResetHard
)

// String 返回 git reset 对应的模式名。
func (m ResetMode) String() string {
	switch m {
	case ResetSoft:
		return "soft"
	case ResetMixed:
		return "mixed"
	case ResetHard:
		return "hard"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(m))
	}
}

// GitReset 把当前分支重置到 commit；soft/mixed 保留工作区，hard 同时重置工作区。
func GitReset(ctx context.Context, repo string, commit Commit, mode ResetMode) (string, error) {
	if mode != ResetSoft && mode != ResetMixed && mode != ResetHard {
		return "", fmt.Errorf("unknown reset mode: %d", mode)
	}
	output, err := gitOutput(ctx, repo, nil, "reset", "--"+mode.String(), commit.Hash)
	if err != nil {
		return "", fmt.Errorf("git reset error: %w", gitStderr(err))
	}
	return output, nil
}

// GitRebase 把当前分支 rebase 到 commit，并自动暂存和恢复本地改动。
func GitRebase(ctx context.Context, repo string, commit Commit) (string, error) {
	output, err := gitOutput(ctx, repo, nil, "rebase", "--autostash", commit.Hash)
	if err != nil {
		return "", fmt.Errorf("git rebase error: %w", gitStderr(err))
	}
	return output, nil
}

// RebaseAction 是 interactive rebase todo 中每个提交的动作。
type RebaseAction string

const (
	RebasePick   RebaseAction = "pick"
	RebaseSquash RebaseAction = "squash"
	RebaseFixup  RebaseAction = "fixup"
	RebaseDrop   RebaseAction = "drop"
)

// rebaseSequenceEditor 把 git 生成的 todo 行首动作替换成 actions 里记录的动作。
// todo 使用缩写 hash，按完整 hash 的前缀匹配；找不到对应动作的行保持原样。
const rebaseSequenceEditor = `#!/bin/sh
set -e
todo="$1"
tmp="$todo.lanegraph"
awk -v map="$LANEGRAPH_REBASE_ACTIONS" '
BEGIN {
    while ((getline line < map) > 0) {
        split(line, fields, " ")
        if (fields[1] != "") actions[fields[1]] = fields[2]
    }
}
{
    if (NF >= 2) {
        for (hash in actions) {
            if (substr(hash, 1, length($2)) == $2) {
                $1 = actions[hash]
                break
            }
        }
    }
    print
}' "$todo" > "$tmp"
mv "$tmp" "$todo"
`

// GitRebaseInteractive 以 commit 为基点执行 interactive rebase，并自动暂存和恢复本地改动。
// actions 按完整 hash 指定每个提交的动作，未指定的提交保留 git 默认的 pick；
// 动作通过临时 sequence editor 写回 todo，GIT_EDITOR=true 让 squash 直接接受合并后的提交信息。
func GitRebaseInteractive(ctx context.Context, repo string, commit Commit, actions map[string]RebaseAction) (string, error) {
	dir, err := os.MkdirTemp("", "lanegraph-rebase-")
	if err != nil {
		return "", fmt.Errorf("interactive rebase error: %w", err)
	}
	defer os.RemoveAll(dir)

	var todo strings.Builder
	for hash, action := range actions {
		fmt.Fprintf(&todo, "%s %s\n", hash, action)
	}
	actionsPath := filepath.Join(dir, "actions")
	if err := os.WriteFile(actionsPath, []byte(todo.String()), 0o600); err != nil {
		return "", fmt.Errorf("interactive rebase error: %w", err)
	}

	editorPath := filepath.Join(dir, "sequence-editor")
	if err := os.WriteFile(editorPath, []byte(rebaseSequenceEditor), 0o700); err != nil {
		return "", fmt.Errorf("interactive rebase error: %w", err)
	}
	// GIT_SEQUENCE_EDITOR 由 git 通过 shell 执行，路径用单引号包裹。
	editorPath = "'" + strings.ReplaceAll(editorPath, "'", "'\\''") + "'"

	env := append(gitContinueEnv(),
		"GIT_SEQUENCE_EDITOR="+editorPath,
		"LANEGRAPH_REBASE_ACTIONS="+actionsPath,
	)
	output, err := gitOutput(ctx, repo, env, "rebase", "-i", "--autostash", commit.Hash)
	if err != nil {
		return "", fmt.Errorf("git rebase error: %w", gitStderr(err))
	}
	return output, nil
}

// rewordSequenceEditor 在 todo 的目标提交后插入 exec，用新消息 amend 该提交。
// 冲突恢复后 git rebase --continue 会继续执行这条 exec，新消息不会丢失。
const rewordSequenceEditor = `#!/bin/sh
set -e
todo="$1"
tmp="$todo.lanegraph"
awk -v hash="$LANEGRAPH_REWORD_HASH" -v cmd="$LANEGRAPH_REWORD_CMD" '
{
    print
    if (NF >= 2 && substr(hash, 1, length($2)) == $2) {
        print "exec " cmd
    }
}' "$todo" > "$tmp"
mv "$tmp" "$todo"
`

// GitReword 改写 commit 的提交信息；commit 必须是当前分支上的非 merge 提交。
// 新消息写入 git 目录，冲突后 continue 仍能通过 todo 里的 exec 读取。
func GitReword(ctx context.Context, repo string, commit Commit, subject, description string) (string, error) {
	if len(commit.Parents) > 1 {
		return "", fmt.Errorf("cannot reword a merge commit")
	}

	dir, err := os.MkdirTemp("", "lanegraph-reword-")
	if err != nil {
		return "", fmt.Errorf("reword error: %w", err)
	}
	defer os.RemoveAll(dir)

	messagePath, ok := gitPath(ctx, repo, "lanegraph-reword-message")
	if !ok {
		return "", fmt.Errorf("reword error: cannot resolve git directory")
	}
	message := subject + "\n"
	if description != "" {
		message += "\n" + description + "\n"
	}
	if err := os.WriteFile(messagePath, []byte(message), 0o600); err != nil {
		return "", fmt.Errorf("reword error: %w", err)
	}

	editorPath := filepath.Join(dir, "sequence-editor")
	if err := os.WriteFile(editorPath, []byte(rewordSequenceEditor), 0o700); err != nil {
		return "", fmt.Errorf("reword error: %w", err)
	}
	// GIT_SEQUENCE_EDITOR 和 exec 都由 git 通过 shell 执行，路径用单引号包裹。
	editorPath = "'" + strings.ReplaceAll(editorPath, "'", "'\\''") + "'"
	messagePath = "'" + strings.ReplaceAll(messagePath, "'", "'\\''") + "'"
	env := append(os.Environ(),
		"GIT_SEQUENCE_EDITOR="+editorPath,
		"LANEGRAPH_REWORD_HASH="+commit.Hash,
		"LANEGRAPH_REWORD_CMD=git commit --amend --no-verify -F "+messagePath,
	)

	args := []string{"rebase", "-i", "--autostash", "--empty=keep"}
	if len(commit.Parents) == 0 {
		args = append(args, "--root")
	} else {
		args = append(args, commit.Parents[0])
	}

	output, err := gitOutput(ctx, repo, env, args...)
	if err != nil {
		if !GitRebaseInProgress(ctx, repo) {
			removeRewordMessage(ctx, repo)
		}
		return "", fmt.Errorf("git rebase error: %w", gitStderr(err))
	}
	removeRewordMessage(ctx, repo)
	return output, nil
}

// GitRebaseInProgress 返回仓库是否停在未完成的 rebase 中。
// rebase-merge 和 rebase-apply 是两种后端；rebase-apply 下的 applying 表示 git am，不算 rebase。
func GitRebaseInProgress(ctx context.Context, repo string) bool {
	if gitPathExists(ctx, repo, "rebase-merge") {
		return true
	}
	return gitPathExists(ctx, repo, "rebase-apply") && !gitPathExists(ctx, repo, "rebase-apply/applying")
}

// GitRebaseContinue 在冲突解决并暂存后继续 rebase，返回 git 的输出。
func GitRebaseContinue(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitContinueEnv(), "rebase", "--continue")
	if err != nil {
		return "", fmt.Errorf("git rebase continue error: %w", gitStderr(err))
	}
	if !GitRebaseInProgress(ctx, repo) {
		removeRewordMessage(ctx, repo)
	}
	return output, nil
}

// GitRebaseAbort 取消未完成的 rebase，恢复到 rebase 之前的状态。
func GitRebaseAbort(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, nil, "rebase", "--abort")
	if err != nil {
		return "", fmt.Errorf("git rebase abort error: %w", gitStderr(err))
	}
	removeRewordMessage(ctx, repo)
	return output, nil
}

// removeRewordMessage 清理 reword 使用的临时消息文件。
func removeRewordMessage(ctx context.Context, repo string) {
	if path, ok := gitPath(ctx, repo, "lanegraph-reword-message"); ok {
		os.Remove(path)
	}
}

// MergeMode 是 git merge 的合并方式。
type MergeMode uint8

const (
	MergeFF MergeMode = iota
	MergeNoFF
	MergeSquash
)

// String 返回 git merge 对应的模式名。
func (m MergeMode) String() string {
	switch m {
	case MergeFF:
		return "ff"
	case MergeNoFF:
		return "no-ff"
	case MergeSquash:
		return "squash"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(m))
	}
}

// GitMerge 按 mode 把 commit 合入当前分支，并自动暂存和恢复本地改动。
// squash 成功后立即提交，不留待提交状态；冲突时保留 SQUASH_MSG，由 Continue / Abort 处理。
func GitMerge(ctx context.Context, repo string, commit Commit, mode MergeMode) (string, error) {
	if mode != MergeFF && mode != MergeNoFF && mode != MergeSquash {
		return "", fmt.Errorf("unknown merge mode: %d", mode)
	}
	output, err := gitOutput(ctx, repo, nil, "merge", "--"+mode.String(), "--no-edit", "--autostash", commit.Hash)
	if err != nil {
		return "", fmt.Errorf("git merge error: %w", gitStderr(err))
	}
	if mode != MergeSquash || !GitSquashInProgress(ctx, repo) {
		return output, nil
	}
	committed, err := GitSquashContinue(ctx, repo)
	if err != nil {
		return "", err
	}
	return output + committed, nil
}

// GitMergeInProgress 返回仓库是否停在未完成的 merge 中。
func GitMergeInProgress(ctx context.Context, repo string) bool {
	return gitPathExists(ctx, repo, "MERGE_HEAD")
}

// GitMergeContinue 在冲突解决并暂存后继续 merge，返回 git 的输出。
func GitMergeContinue(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitContinueEnv(), "merge", "--continue")
	if err != nil {
		return "", fmt.Errorf("git merge continue error: %w", gitStderr(err))
	}
	return output, nil
}

// GitMergeAbort 取消未完成的 merge，恢复到 merge 之前的状态。
func GitMergeAbort(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, nil, "merge", "--abort")
	if err != nil {
		return "", fmt.Errorf("git merge abort error: %w", gitStderr(err))
	}
	return output, nil
}

// GitSquashInProgress 返回是否有待提交的 squash merge。
// squash 用 SQUASH_MSG 记录状态，不会生成 MERGE_HEAD。
func GitSquashInProgress(ctx context.Context, repo string) bool {
	return gitPathExists(ctx, repo, "SQUASH_MSG")
}

// GitSquashContinue 提交 squash 结果，使用 git 生成的 squash 提交信息；
// squash merge 成功时由 GitMerge 自动调用，冲突或自动提交失败时由用户 Continue；
// 有 autostash 时 git commit 会自动恢复本地改动。
func GitSquashContinue(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitContinueEnv(), "commit", "--no-edit")
	if err != nil {
		return "", fmt.Errorf("git commit error: %w", gitStderr(err))
	}
	return output, nil
}

// GitSquashAbort 取消待提交的 squash，恢复到 merge 之前的状态。
// squash 没有 MERGE_HEAD，git merge --abort 不可用；reset --merge 会保留未暂存的本地改动，
// autostash 则会被重新放进 stash，需要再 pop 出来。
func GitSquashAbort(ctx context.Context, repo string) (string, error) {
	autostash := ""
	if path, ok := gitPath(ctx, repo, "MERGE_AUTOSTASH"); ok {
		if data, err := os.ReadFile(path); err == nil {
			autostash = strings.TrimSpace(string(data))
		}
	}

	output, err := gitOutput(ctx, repo, nil, "reset", "--merge", "ORIG_HEAD")
	if err != nil {
		return "", fmt.Errorf("git reset error: %w", gitStderr(err))
	}
	if autostash == "" {
		return output, nil
	}

	// reset 会把 autostash 重新存成一个新的 stash 条目；确认栈顶就是它再 pop，
	// 避免误动用户自己的 stash。
	if stash, err := gitOutputTrimmed(ctx, repo, "rev-parse", "--verify", "refs/stash"); err == nil && stash == autostash {
		popped, err := gitOutput(ctx, repo, nil, "stash", "pop")
		if err != nil {
			return output, fmt.Errorf("git stash pop error: %w", gitStderr(err))
		}
		output += popped
	}
	return output, nil
}

// cherryPickAutostash 是 cherry-pick 自动暂存使用的标记，作用类似 git 的 MERGE_AUTOSTASH。
const cherryPickAutostash = "LANEGRAPH_CHERRY_PICK_AUTOSTASH"

// GitCherryPick 把 commit 应用到当前分支，并自动暂存和恢复本地改动。
func GitCherryPick(ctx context.Context, repo string, commit Commit) (string, error) {
	if err := gitAutostash(ctx, repo, cherryPickAutostash); err != nil {
		return "", err
	}

	output, err := gitOutput(ctx, repo, nil, "cherry-pick", commit.Hash)
	if err != nil {
		// 只有停在冲突态时才把 stash 留给 Continue / Abort 恢复。
		if !GitCherryPickInProgress(ctx, repo) {
			restored, _ := gitUnstash(ctx, repo, cherryPickAutostash)
			output += restored
		}
		return output, fmt.Errorf("git cherry-pick error: %w", gitStderr(err))
	}

	restored, _ := gitUnstash(ctx, repo, cherryPickAutostash)
	return output + restored, nil
}

// GitCherryPickInProgress 返回仓库是否停在未完成的 cherry-pick 中。
func GitCherryPickInProgress(ctx context.Context, repo string) bool {
	return gitPathExists(ctx, repo, "CHERRY_PICK_HEAD")
}

// GitCherryPickContinue 在冲突解决并暂存后继续 cherry-pick，返回 git 的输出；完成后恢复之前暂存的本地改动。
func GitCherryPickContinue(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitContinueEnv(), "cherry-pick", "--continue")
	if err != nil {
		return "", fmt.Errorf("git cherry-pick continue error: %w", gitStderr(err))
	}
	restored, _ := gitUnstash(ctx, repo, cherryPickAutostash)
	return output + restored, nil
}

// GitCherryPickAbort 取消未完成的 cherry-pick，恢复到 cherry-pick 之前的状态；完成后恢复之前暂存的本地改动。
func GitCherryPickAbort(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, nil, "cherry-pick", "--abort")
	if err != nil {
		return "", fmt.Errorf("git cherry-pick abort error: %w", gitStderr(err))
	}
	restored, _ := gitUnstash(ctx, repo, cherryPickAutostash)
	return output + restored, nil
}

// gitAutostash 暂存本地改动，并把 stash hash 记录到 name 指定的 git 路径。
// 没有本地改动时不写记录；name 同时用作 stash 列表里的标记。
func gitAutostash(ctx context.Context, repo, name string) error {
	before, _ := gitOutputTrimmed(ctx, repo, "rev-parse", "--verify", "refs/stash")
	if _, err := gitOutput(ctx, repo, nil, "stash", "push", "-m", name); err != nil {
		return fmt.Errorf("git stash push error: %w", gitStderr(err))
	}
	hash, err := gitOutputTrimmed(ctx, repo, "rev-parse", "--verify", "refs/stash")
	if err != nil || hash == "" || hash == before {
		return nil
	}

	path, ok := gitPath(ctx, repo, name)
	if !ok {
		_, _ = gitOutput(ctx, repo, nil, "stash", "pop")
		return errors.New("git path not found: " + name)
	}
	if err := os.WriteFile(path, []byte(hash+"\n"), 0o600); err != nil {
		// 记录失败时立即恢复改动，避免它滞留在 stash 里。
		_, _ = gitOutput(ctx, repo, nil, "stash", "pop")
		return fmt.Errorf("write autostash error: %w", err)
	}
	return nil
}

// gitUnstash 恢复 name 记录的本地改动并删除记录。
// 恢复冲突时保留 stash 条目，和 git --autostash 的行为一致，由用户自行处理。
func gitUnstash(ctx context.Context, repo, name string) (string, error) {
	path, ok := gitPath(ctx, repo, name)
	if !ok {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read autostash error: %w", err)
	}
	// 先清掉记录：即使恢复失败，也不能影响之后的操作。
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove autostash error: %w", err)
	}

	hash := strings.TrimSpace(string(data))
	if hash == "" {
		return "", nil
	}

	var output string
	if top, topErr := gitOutputTrimmed(ctx, repo, "rev-parse", "--verify", "refs/stash"); topErr == nil && top == hash {
		output, err = gitOutput(ctx, repo, nil, "stash", "pop")
	} else {
		output, err = gitOutput(ctx, repo, nil, "stash", "apply", hash)
	}
	if err != nil {
		return output, fmt.Errorf("git stash restore error: %w", gitStderr(err))
	}
	return output, nil
}

// gitPathExists 判断 git 路径是否存在。
func gitPathExists(ctx context.Context, repo, name string) bool {
	path, ok := gitPath(ctx, repo, name)
	if !ok {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// gitPath 返回 git 路径；路径由 git 解析，兼容 worktree 的独立 gitdir。
func gitPath(ctx context.Context, repo, name string) (string, bool) {
	path, err := gitOutputTrimmed(ctx, repo, "rev-parse", "--git-path", name)
	if err != nil || path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	return path, true
}

// gitContinueEnv 返回继续 rebase / merge 时使用的环境：
// 解决冲突后 --continue 会打开提交信息编辑器，TUI 独占终端，直接接受默认信息。
func gitContinueEnv() []string {
	return append(os.Environ(), "GIT_EDITOR=true")
}

// GitCommit 用 subject 和可选的 description 提交暂存区改动；amend 为 true 时改写 HEAD 提交。
func GitCommit(ctx context.Context, repo, subject, description string, amend bool) (string, error) {
	args := []string{"commit", "-m", subject}
	if description != "" {
		args = append(args, "-m", description)
	}
	if amend {
		args = append(args, "--amend")
	}
	output, err := gitOutput(ctx, repo, nil, args...)
	if err != nil {
		return "", fmt.Errorf("git commit error: %w", gitStderr(err))
	}
	return output, nil
}

// ChangeScope 标记变更的来源；同一文件可能同时出现在暂存区和未暂存区。
type ChangeScope uint8

const (
	ScopeCommit ChangeScope = iota // commit diff 用零值，保持原有行为
	ScopeStaged
	ScopeUnstaged
	ScopeUntracked
)

type FileChange struct {
	Status  string // git name-status，如 A、M、D、R100；未跟踪文件为 ?
	Path    string
	OldPath string
	Scope   ChangeScope
}

// paths 返回需要传给 git 的路径；重命名/复制同时包含旧路径和新路径。
func (c FileChange) paths() []string {
	if c.OldPath != "" {
		return []string{c.OldPath, c.Path}
	}
	return []string{c.Path}
}

// GitCommitFiles 相对 compare 计算文件变更；compare 为 nil 时相对第一个 parent。
// 根提交没有 parent 且没有 compare，只能把全部文件视为新增。
func GitCommitFiles(ctx context.Context, repo string, commit Commit, compare *Commit) ([]FileChange, error) {
	var args []string
	if base := commit.diffBase(compare); base != "" {
		args = []string{"diff", "--name-status", "-z", "-M", base, commit.Hash}
	} else {
		args = []string{"show", "--format=", "--name-status", "-z", "-M", commit.Hash}
	}
	diff, err := gitOutput(ctx, repo, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("git commit files error: %w", err)
	}
	return parseNameStatus(diff, ScopeCommit), nil
}

// GitLocalChanges 返回本地工作区的全部变更：
// 暂存区（index vs HEAD）、未暂存（worktree vs index）和未跟踪文件。
func GitLocalChanges(ctx context.Context, repo string) ([]FileChange, error) {
	staged, err := gitOutput(ctx, repo, nil, "diff", "--cached", "--name-status", "-z", "-M")
	if err != nil {
		return nil, fmt.Errorf("git staged changes error: %w", err)
	}

	unstaged, err := gitOutput(ctx, repo, nil, "diff", "--name-status", "-z", "-M")
	if err != nil {
		return nil, fmt.Errorf("git unstaged changes error: %w", err)
	}

	untracked, err := gitOutput(ctx, repo, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("git untracked files error: %w", err)
	}

	changes := parseNameStatus(staged, ScopeStaged)
	changes = append(changes, parseNameStatus(unstaged, ScopeUnstaged)...)
	for _, path := range strings.Split(untracked, "\x00") {
		if path == "" {
			continue
		}
		changes = append(changes, FileChange{Status: "?", Path: path, Scope: ScopeUntracked})
	}
	return changes, nil
}

// parseNameStatus 解析 git --name-status -z 的输出：
// 每项为 status\0path\0，R/C 状态后跟 oldPath\0newPath\0。
func parseNameStatus(output string, scope ChangeScope) []FileChange {
	fields := strings.Split(output, "\x00")
	var changes []FileChange
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" || i >= len(fields) {
			continue
		}

		change := FileChange{Status: status, Path: fields[i], Scope: scope}
		i++
		if status[0] == 'R' || status[0] == 'C' {
			if i >= len(fields) {
				break
			}
			change.OldPath = change.Path
			change.Path = fields[i]
			i++
		}

		changes = append(changes, change)
	}
	return changes
}

// DiffOptions 是渲染单个文件 diff 时的展示选项。
// 它只作为方法参数使用，由调用方按当前状态动态组装，不保存在 State 中。
type DiffOptions struct {
	IgnoreWhitespace bool
	SideBySide       bool
	// Width 是 side-by-side 布局的目标宽度（面板内宽）；<=0 时使用默认宽度。
	Width int
}

// String 把影响 diff 渲染结果的选项编码成稳定的缓存 key。
func (o DiffOptions) String() string {
	return fmt.Sprintf("%t\x00%t\x00%d", o.IgnoreWhitespace, o.SideBySide, o.Width)
}

// GitFileDiff 相对 compare 计算单个文件的 diff，并交给 git-delta 着色。
// compare 为 nil 时相对第一个 parent；options.IgnoreWhitespace 为 true 时用 -w 忽略纯空白改动；
// git-delta 失败时返回原始 diff，由调用方决定如何展示。
func GitFileDiff(ctx context.Context, repo string, commit Commit, compare *Commit, change FileChange, options DiffOptions) (string, error) {
	var args []string
	if base := commit.diffBase(compare); base != "" {
		args = diffArgs([]string{"diff", "-M", "--no-color", "--no-ext-diff"}, options.IgnoreWhitespace, base, commit.Hash, "--")
	} else {
		args = diffArgs([]string{"show", "--format=", "-M", "--no-color", "--no-ext-diff"}, options.IgnoreWhitespace, commit.Hash, "--")
	}
	// 重命名/复制需要同时限制新旧路径，否则 git 会把新路径识别成新增文件
	if change.OldPath != "" {
		args = append(args, change.OldPath)
	}
	args = append(args, change.Path)

	diff, err := gitOutput(ctx, repo, nil, args...)
	if err != nil {
		return "", fmt.Errorf("git file diff error: %w", err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return colorizeDiff(ctx, diff, options)
}

// GitLocalFileDiff 计算本地单个文件的 diff：暂存区相对 HEAD、工作区相对暂存区，
// 未跟踪文件则与 /dev/null 对比，生成可直接展示的新增 diff。
// 已跟踪文件按 options.IgnoreWhitespace 决定是否用 -w 忽略纯空白改动；
// 未跟踪文件与 /dev/null 对比，无需该参数。
func GitLocalFileDiff(ctx context.Context, repo string, change FileChange, options DiffOptions) (string, error) {
	var diff string
	var err error

	switch change.Scope {
	case ScopeStaged:
		diff, err = gitWorkingDiff(ctx, repo, change, diffArgs([]string{"diff", "--cached", "-M"}, options.IgnoreWhitespace)...)
	case ScopeUnstaged:
		diff, err = gitWorkingDiff(ctx, repo, change, diffArgs([]string{"diff", "-M"}, options.IgnoreWhitespace)...)
	case ScopeUntracked:
		diff, err = gitDiffNoIndex(ctx, repo, change.Path)
	default:
		return "", fmt.Errorf("unknown change scope: %d", change.Scope)
	}
	if err != nil {
		return "", fmt.Errorf("git local diff error: %w", err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return colorizeDiff(ctx, diff, options)
}

// diffArgs 生成 git diff 参数：ignoreWhitespace 为 true 时在路径分隔符前插入 -w。
func diffArgs(command []string, ignoreWhitespace bool, rest ...string) []string {
	args := make([]string, 0, len(command)+len(rest)+1)
	args = append(args, command...)
	if ignoreWhitespace {
		args = append(args, "-w")
	}
	return append(args, rest...)
}

// gitWorkingDiff 限制到单个文件；重命名/复制需要同时传入旧路径，否则会被识别成新增文件。
func gitWorkingDiff(ctx context.Context, repo string, change FileChange, base ...string) (string, error) {
	args := append(base, "--no-color", "--no-ext-diff", "--")
	if change.OldPath != "" {
		args = append(args, change.OldPath)
	}
	args = append(args, change.Path)
	return gitOutput(ctx, repo, nil, args...)
}

// gitDiffNoIndex 生成未跟踪文件相对 /dev/null 的 diff。
// git diff --no-index 在文件有差异时退出码为 1，这属于正常结果。
func gitDiffNoIndex(ctx context.Context, repo, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-color", "/dev/null", "--", path)
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return string(output), nil
		}
		return "", err
	}
	return string(output), nil
}

// GitStage 把 changes 对应的改动加入暂存区；删除和重命名也会一并在索引中更新。
func GitStage(ctx context.Context, repo string, changes []FileChange) error {
	paths := changePaths(changes)
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return fmt.Errorf("git stage error: %w", gitStderr(err))
	}
	return nil
}

// GitUnstage 把 changes 从暂存区移回工作区。
func GitUnstage(ctx context.Context, repo string, changes []FileChange) error {
	paths := changePaths(changes)
	if len(paths) == 0 {
		return nil
	}

	// 空仓库还没有 HEAD，reset 无法执行，只能把文件从索引里移除。
	if !gitRefExists(ctx, repo, "HEAD") {
		args := append([]string{"rm", "-r", "--cached", "-q", "--"}, paths...)
		if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
			return fmt.Errorf("git unstage error: %w", gitStderr(err))
		}
		return nil
	}

	args := append([]string{"reset", "-q", "HEAD", "--"}, paths...)
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return fmt.Errorf("git unstage error: %w", gitStderr(err))
	}
	return nil
}

// changePaths 汇总多个变更需要传给 git 的路径，跳过没有路径的占位项。
func changePaths(changes []FileChange) []string {
	var paths []string
	for _, change := range changes {
		if change.Path == "" {
			continue
		}
		paths = append(paths, change.paths()...)
	}
	return paths
}

// GitDiscard 丢弃 change 对应的改动：
// 未跟踪文件直接删除，未暂存改动恢复成索引版本，已暂存改动重置回 HEAD。
func GitDiscard(ctx context.Context, repo string, change FileChange) error {
	if change.Path == "" {
		return nil
	}
	switch change.Scope {
	case ScopeUntracked:
		return removeWorktree(repo, change.Path)
	case ScopeUnstaged:
		return discardWorktree(ctx, repo, change)
	case ScopeStaged:
		return discardStaged(ctx, repo, change)
	default:
		return fmt.Errorf("unknown change scope: %d", change.Scope)
	}
}

// discardWorktree 丢弃工作区改动；重命名需要恢复旧路径并删除新路径。
func discardWorktree(ctx context.Context, repo string, change FileChange) error {
	if change.OldPath != "" {
		if err := restoreWorktree(ctx, repo, change.OldPath); err != nil {
			return err
		}
		return removeWorktree(repo, change.Path)
	}
	return restoreWorktree(ctx, repo, change.Path)
}

// discardStaged 把文件重置回 HEAD：先重置索引，再按状态恢复或删除工作区文件。
func discardStaged(ctx context.Context, repo string, change FileChange) error {
	if !gitRefExists(ctx, repo, "HEAD") {
		args := append([]string{"rm", "-r", "--cached", "-q", "--"}, change.paths()...)
		if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
			return fmt.Errorf("git discard error: %w", gitStderr(err))
		}
		return removeWorktree(repo, change.Path)
	}

	args := append([]string{"reset", "-q", "HEAD", "--"}, change.paths()...)
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return fmt.Errorf("git discard error: %w", gitStderr(err))
	}

	switch {
	case change.OldPath != "":
		if err := restoreWorktree(ctx, repo, change.OldPath); err != nil {
			return err
		}
		return removeWorktree(repo, change.Path)
	case change.Status != "" && change.Status[0] == 'A':
		// 已暂存的新文件在 HEAD 里不存在，重置索引后还要删掉工作区文件。
		return removeWorktree(repo, change.Path)
	default:
		return restoreWorktree(ctx, repo, change.Path)
	}
}

// restoreWorktree 用索引里的版本覆盖工作区文件。
func restoreWorktree(ctx context.Context, repo string, paths ...string) error {
	args := append([]string{"restore", "--worktree", "--"}, paths...)
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return fmt.Errorf("git restore error: %w", gitStderr(err))
	}
	return nil
}

// removeWorktree 删除工作区文件；文件已不存在时视为成功。
func removeWorktree(repo, path string) error {
	err := os.Remove(filepath.Join(repo, path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove file error: %w", err)
	}
	return nil
}

func gitRefExists(ctx context.Context, repo, ref string) bool {
	_, err := gitOutputTrimmed(ctx, repo, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// gitStderr 取出 git 写进 stderr 的原文（如未提交改动冲突），
// 否则调用方只能看到 "exit status 1"。
func gitStderr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if message := strings.TrimSpace(string(exitErr.Stderr)); message != "" {
			return errors.New(message)
		}
	}
	return err
}

func gitOutputTrimmed(ctx context.Context, repo string, args ...string) (string, error) {
	output, err := gitOutput(ctx, repo, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// gitOutput 保留原始输出，供 -z 分隔的路径解析使用，避免 TrimSpace 破坏文件名。
// env 为 nil 时继承当前进程环境。
func gitOutput(ctx context.Context, repo string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return string(output), nil
}

// colorizeDiff 把原始 diff 交给 git-delta 着色；
// side-by-side 使用固定宽度（面板内宽）并无限折行以完整展示内容，其余情况保持 variable 宽度。
// git-delta 失败时返回原始 diff，由调用方决定如何展示。
func colorizeDiff(ctx context.Context, diff string, options DiffOptions) (string, error) {
	args := []string{
		"--paging=never", "--no-gitconfig", "--true-color=always",
		"--line-numbers", "--file-style=omit",
		"--hunk-header-decoration-style=gray box",
		"--hunk-header-line-number-style=gray",
		"--whitespace-error-style=magenta dim",
		"--line-numbers-left-style=gray",
		"--line-numbers-right-style=gray",
		"--line-numbers-zero-style=gray",
		"--inline-hint-style=gray",
		// 压暗 +/− 行的整行底色；强调片段仍由 delta 的 emph 样式自动提亮
		"--minus-style=syntax \"#3b2323\"",
		"--plus-style=syntax \"#20352a\"",
	}
	if options.SideBySide {
		width := options.Width
		if width <= 0 {
			width = 80
		}
		args = append(args,
			"--side-by-side",
			fmt.Sprintf("--width=%d", width),
			"--wrap-max-lines=unlimited",
		)
	} else {
		args = append(args, "--width=variable")
	}

	cmd := exec.CommandContext(ctx, "delta", args...)
	cmd.Stdin = strings.NewReader(diff)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return diff, fmt.Errorf("git-delta error: %w", err)
	}
	return string(output), nil
}
