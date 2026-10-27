package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func GitRepoName(ctx context.Context, repo string) string {
	root, err := gitOutput(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return filepath.Base(repo)
	}
	return filepath.Base(root)
}

func GitRepoHead(ctx context.Context, repo string) (string, string) {
	hash, _ := gitOutput(ctx, repo, "rev-parse", "HEAD")

	// HEAD 指向分支时直接显示分支名
	if ref, err := gitOutput(ctx, repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && ref != "" {
		return ref, hash
	}
	// detached HEAD：优先显示指向该提交的 tag，其次本地分支
	for _, pattern := range []string{"refs/tags", "refs/heads"} {
		if ref, err := gitOutput(ctx, repo, "for-each-ref", "--points-at", "HEAD", "--count=1", "--format=%(refname:short)", pattern); err == nil && ref != "" {
			return ref, hash
		}
	}
	// 没有 ref 可显示时回退到 7 位短 hash
	if len(hash) > 7 {
		return hash[:7], hash
	}
	if hash != "" {
		return hash, hash
	}
	return "HEAD", ""
}

type Branch struct {
	Name   string
	Hash   string
	Remote bool
}

func GitBranches(ctx context.Context, repo string) ([]Branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	output, err := gitOutput(ctx, repo, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var branches []Branch
	for line := range strings.Lines(output) {
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
		branches = append(branches, Branch{
			Name:   parts[1],
			Hash:   parts[2],
			Remote: strings.HasPrefix(parts[0], "refs/remotes/"),
		})
	}
	return branches, nil
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

func GitCommits(ctx context.Context, repo string) ([]Commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
	// -z 用 NUL 分隔记录，body 里的换行才不会把一条记录拆成多行
	output, err := gitOutput(ctx, repo, "log", "-z", "--date-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%an%x00%ct%x00%s%x00%b")
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	// 字段和记录都以 NUL 分隔，每 7 段是一个 commit；末尾终止符会多出一个空段
	parts := strings.Split(output, "\x00")
	var commits []Commit
	for i := 0; i+7 <= len(parts); i += 7 {
		if parts[i] == "" {
			continue
		}
		date := time.Unix(0, 0)
		if sec, err := strconv.ParseInt(parts[i+4], 10, 64); err == nil {
			date = time.Unix(sec, 0)
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

// GitCommitFiles 相对第一个 parent 计算文件变更；根提交没有 parent，只能把全部文件视为新增。
func GitCommitFiles(ctx context.Context, repo string, commit Commit) ([]FileChange, error) {
	var args []string
	if len(commit.Parents) > 0 {
		args = []string{"diff", "--name-status", "-z", "-M", commit.Parents[0], commit.Hash}
	} else {
		args = []string{"show", "--format=", "--name-status", "-z", "-M", commit.Hash}
	}
	output, err := gitOutputUntrimmed(ctx, repo, args...)
	if err != nil {
		return nil, fmt.Errorf("git commit files error: %w", err)
	}
	return parseNameStatus(output, ScopeCommit), nil
}

// GitLocalChanges 返回本地工作区的全部变更：
// 暂存区（index vs HEAD）、未暂存（worktree vs index）和未跟踪文件。
func GitLocalChanges(ctx context.Context, repo string) ([]FileChange, error) {
	staged, err := gitOutputUntrimmed(ctx, repo, "diff", "--cached", "--name-status", "-z", "-M")
	if err != nil {
		return nil, fmt.Errorf("git staged changes error: %w", err)
	}

	unstaged, err := gitOutputUntrimmed(ctx, repo, "diff", "--name-status", "-z", "-M")
	if err != nil {
		return nil, fmt.Errorf("git unstaged changes error: %w", err)
	}

	untracked, err := gitOutputUntrimmed(ctx, repo, "ls-files", "--others", "--exclude-standard", "-z")
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

// GitFileDiff 相对第一个 parent 计算单个文件的 diff，并交给 git-delta 着色。
// git-delta 失败时返回原始 diff，由调用方决定如何展示。
func GitFileDiff(ctx context.Context, repo string, commit Commit, change FileChange) (string, error) {
	var args []string
	if len(commit.Parents) > 0 {
		args = []string{"diff", "-M", "--no-color", "--no-ext-diff", commit.Parents[0], commit.Hash, "--"}
	} else {
		args = []string{"show", "--format=", "-M", "--no-color", "--no-ext-diff", commit.Hash, "--"}
	}
	// 重命名/复制需要同时限制新旧路径，否则 git 会把新路径识别成新增文件
	if change.OldPath != "" {
		args = append(args, change.OldPath)
	}
	args = append(args, change.Path)

	diff, err := gitOutputUntrimmed(ctx, repo, args...)
	if err != nil {
		return "", fmt.Errorf("git file diff error: %w", err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return colorizeDiff(ctx, diff)
}

// GitLocalFileDiff 计算本地单个文件的 diff：暂存区相对 HEAD、工作区相对暂存区，
// 未跟踪文件则与 /dev/null 对比，生成可直接展示的新增 diff。
func GitLocalFileDiff(ctx context.Context, repo string, change FileChange) (string, error) {
	var diff string
	var err error

	switch change.Scope {
	case ScopeStaged:
		diff, err = gitWorkingDiff(ctx, repo, change, "diff", "--cached", "-M")
	case ScopeUnstaged:
		diff, err = gitWorkingDiff(ctx, repo, change, "diff", "-M")
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
	return colorizeDiff(ctx, diff)
}

// gitWorkingDiff 限制到单个文件；重命名/复制需要同时传入旧路径，否则会被识别成新增文件。
func gitWorkingDiff(ctx context.Context, repo string, change FileChange, base ...string) (string, error) {
	args := append(base, "--no-color", "--no-ext-diff", "--")
	if change.OldPath != "" {
		args = append(args, change.OldPath)
	}
	args = append(args, change.Path)
	return gitOutputUntrimmed(ctx, repo, args...)
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

func gitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	output, err := gitOutputUntrimmed(ctx, repo, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// gitOutputUntrimmed 保留原始输出，供 -z 分隔的路径解析使用，避免 TrimSpace 破坏文件名。
func gitOutputUntrimmed(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
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
// git-delta 失败时返回原始 diff，由调用方决定如何展示。
func colorizeDiff(ctx context.Context, diff string) (string, error) {
	delta := exec.CommandContext(ctx, "delta",
		"--paging=never", "--no-gitconfig", "--width=variable", "--true-color=always",
		"--line-numbers", "--file-style=omit",
		"--hunk-header-decoration-style=gray box",
		"--hunk-header-line-number-style=gray",
		"--whitespace-error-style=magenta dim",
		"--line-numbers-left-style=gray",
		"--line-numbers-right-style=gray",
		"--line-numbers-zero-style=gray")
	delta.Stdin = strings.NewReader(diff)
	out, err := delta.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return diff, fmt.Errorf("git-delta error: %w", err)
	}
	return string(out), nil
}
