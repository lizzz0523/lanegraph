package core

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func gitOutput(repo string, args ...string) (string, error) {
	output, err := gitOutputUntrimmed(repo, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// gitOutputUntrimmed 保留原始输出，供 -z 分隔的路径解析使用，避免 TrimSpace 破坏文件名。
func gitOutputUntrimmed(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func GitRepoName(repo string) string {
	root, err := gitOutput(repo, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return filepath.Base(repo)
	}
	return filepath.Base(root)
}

func GitHeadRef(repo string) (string, string) {
	hash, _ := gitOutput(repo, "rev-parse", "HEAD")

	// HEAD 指向分支时直接显示分支名
	if ref, err := gitOutput(repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && ref != "" {
		return ref, hash
	}
	// detached HEAD：优先显示指向该提交的 tag，其次本地分支
	for _, pattern := range []string{"refs/tags", "refs/heads"} {
		if ref, err := gitOutput(repo, "for-each-ref", "--points-at", "HEAD", "--count=1", "--format=%(refname:short)", pattern); err == nil && ref != "" {
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

func GitBranches(repo string) ([]Branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	output, err := gitOutput(repo, "for-each-ref", "--sort=-committerdate",
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

func GitCommits(repo string) ([]Commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
	// -z 用 NUL 分隔记录，body 里的换行才不会把一条记录拆成多行
	output, err := gitOutput(repo, "log", "-z", "--date-order", "--branches",
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

type FileChange struct {
	Status  string // git name-status，如 A、M、D、R100
	Path    string
	OldPath string
}

// GitCommitFiles 相对第一个 parent 计算文件变更；根提交没有 parent，只能把全部文件视为新增。
func GitCommitFiles(repo string, commit Commit) ([]FileChange, error) {
	var args []string
	if len(commit.Parents) > 0 {
		args = []string{"diff", "--name-status", "-z", "-M", commit.Parents[0], commit.Hash}
	} else {
		args = []string{"show", "--format=", "--name-status", "-z", "-M", commit.Hash}
	}
	output, err := gitOutputUntrimmed(repo, args...)
	if err != nil {
		return nil, fmt.Errorf("git commit files error: %w", err)
	}
	fields := strings.Split(output, "\x00")
	var changes []FileChange
	// 解析 git --name-status -z 的输出：
	// 每项为 status\0path\0，R/C 状态后跟 oldPath\0newPath\0。
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" || i >= len(fields) {
			continue
		}

		change := FileChange{Status: status, Path: fields[i]}
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
	return changes, nil
}

// GitFileDiff 相对第一个 parent 计算单个文件的 diff，并交给 git-delta 着色。
// git-delta 失败时返回原始 diff，由调用方回退到 buildPlainDiff。
func GitFileDiff(repo string, commit Commit, change FileChange) (string, error) {
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

	diff, err := gitOutputUntrimmed(repo, args...)
	if err != nil {
		return "", fmt.Errorf("git file diff error: %w", err)
	}

	delta := exec.Command("delta",
		"--paging=never", "--no-gitconfig", "--width=variable", "--true-color=always",
		"--line-numbers", "--file-style=omit",
		"--hunk-header-decoration-style=gray box",
		"--hunk-header-line-number-style=gray",
		"--line-numbers-left-style=gray",
		"--line-numbers-right-style=gray",
		"--line-numbers-zero-style=gray")
	delta.Stdin = strings.NewReader(diff)
	out, err := delta.Output()
	if err != nil {
		return diff, fmt.Errorf("git-delta error: %w", err)
	}
	return string(out), nil
}
