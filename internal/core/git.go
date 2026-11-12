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

func GitRepoName(ctx context.Context, repo string) string {
	root, err := gitOutputTrimmed(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return filepath.Base(repo)
	}
	return filepath.Base(root)
}

func GitRepoHead(ctx context.Context, repo string) (string, string) {
	hash, _ := gitOutputTrimmed(ctx, repo, "rev-parse", "HEAD")

	// HEAD 指向分支时直接显示分支名
	if ref, err := gitOutputTrimmed(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && ref != "" {
		return ref, hash
	}
	// detached HEAD：优先显示指向该提交的 tag，其次本地分支
	for _, pattern := range []string{"refs/tags", "refs/heads"} {
		if ref, err := gitOutputTrimmed(ctx, repo, "for-each-ref", "--points-at", "HEAD", "--count=1", "--format=%(refname:short)", pattern); err == nil && ref != "" {
			return ref, hash
		}
	}
	// 没有 ref 可显示时回退到 7 位短 hash
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

// GitCheckout 切换到 branch 对应的本地分支。
// 本地分支直接 checkout；远端分支切换到同名本地分支，
// 本地分支不存在时从该远端分支创建跟踪分支。返回实际检出的本地分支名。
func GitCheckout(ctx context.Context, repo string, branch Branch) (string, error) {
	local := branch.LocalName()

	// 远端分支对应的本地分支已存在时直接切换，不重置、不改 upstream。
	if branch.Remote && !gitRefExists(ctx, repo, "refs/heads/"+local) {
		if _, err := gitOutput(ctx, repo, nil, "checkout", "-b", local, "--track", branch.Name); err != nil {
			return "", fmt.Errorf("git checkout error: %w", gitStderr(err))
		}
		return local, nil
	}

	if _, err := gitOutput(ctx, repo, nil, "checkout", local); err != nil {
		return "", fmt.Errorf("git checkout error: %w", gitStderr(err))
	}
	return local, nil
}

func gitRefExists(ctx context.Context, repo, ref string) bool {
	_, err := gitOutputTrimmed(ctx, repo, "show-ref", "--verify", "--quiet", ref)
	return err == nil
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

// GitPull 拉取当前分支的上游变更，返回 git 的输出。
func GitPull(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitRemoteEnv(), "pull")
	if err != nil {
		return "", fmt.Errorf("git pull error: %w", gitStderr(err))
	}
	return output, nil
}

// GitPush 推送当前分支到它的上游远端，返回 git 的输出。
func GitPush(ctx context.Context, repo string) (string, error) {
	output, err := gitOutput(ctx, repo, gitRemoteEnv(), "push")
	if err != nil {
		return "", fmt.Errorf("git push error: %w", gitStderr(err))
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
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
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

// GitStage 把 change 对应的改动加入暂存区；删除和重命名也会一并在索引中更新。
func GitStage(ctx context.Context, repo string, change FileChange) error {
	if change.Path == "" {
		return nil
	}
	args := append([]string{"add", "-A", "--"}, change.paths()...)
	if _, err := gitOutput(ctx, repo, nil, args...); err != nil {
		return fmt.Errorf("git stage error: %w", gitStderr(err))
	}
	return nil
}

// GitUnstage 把 change 从暂存区移回工作区。
func GitUnstage(ctx context.Context, repo string, change FileChange) error {
	if change.Path == "" {
		return nil
	}
	paths := change.paths()

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
