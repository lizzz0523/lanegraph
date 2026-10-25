package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func main() {
	repo, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 {
		repo = os.Args[1]
	}

	commits, err := gitCommits(repo)
	if err != nil {
		log.Fatal(err)
	}

	branches, err := gitBranches(repo)
	if err != nil {
		log.Fatal(err)
	}

	headRef, headHash := gitHeadRef(repo)

	c := &client{
		repo:      repo,
		repoName:  gitRepoName(repo),
		headRef:   headRef,
		headHash:  headHash,
		commits:   commits,
		branches:  branches,
		fileCache: make(map[string][]fileChange),
		diffCache: make(map[string]string),
	}
	if err := c.run(); err != nil {
		log.Fatal(err)
	}
}

type client struct {
	app *tview.Application

	repo     string
	repoName string
	headRef  string
	headHash string

	branches   []branch
	branchList *tview.TreeView

	commits    []commit
	commitList *laneView

	infoWidth  int
	commitInfo *tview.TextView

	fileList  *tview.TreeView
	fileCache map[string][]fileChange
	filesHash string

	fileDiff  *tview.TextView
	diffCache map[string]string
	diffShown string // 当前已渲染 diff 的 key（commit hash + path），用于跳过重复渲染

	currentCommit commit
	selectedStyle tcell.Style
}

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

func gitRepoName(repo string) string {
	root, err := gitOutput(repo, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return filepath.Base(repo)
	}
	return filepath.Base(root)
}

func gitHeadRef(repo string) (string, string) {
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

type branch struct {
	name   string
	hash   string
	remote bool
}

func gitBranches(repo string) ([]branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	output, err := gitOutput(repo, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var branches []branch
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
		branches = append(branches, branch{
			name:   parts[1],
			hash:   parts[2],
			remote: strings.HasPrefix(parts[0], "refs/remotes/"),
		})
	}
	return branches, nil
}

type commit struct {
	hash    string
	parents []string
	refs    string // %D，如 "HEAD -> main, origin/main, tag: v1"
	author  string
	date    time.Time
	subject string
	body    string
}

func gitCommits(repo string) ([]commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
	// -z 用 NUL 分隔记录，body 里的换行才不会把一条记录拆成多行
	output, err := gitOutput(repo, "log", "-z", "--date-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%an%x00%ct%x00%s%x00%b")
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	// 字段和记录都以 NUL 分隔，每 7 段是一个 commit；末尾终止符会多出一个空段
	parts := strings.Split(output, "\x00")
	var commits []commit
	for i := 0; i+7 <= len(parts); i += 7 {
		if parts[i] == "" {
			continue
		}
		date := time.Unix(0, 0)
		if sec, err := strconv.ParseInt(parts[i+4], 10, 64); err == nil {
			date = time.Unix(sec, 0)
		}
		commits = append(commits, commit{
			hash:    parts[i],
			parents: strings.Fields(parts[i+1]),
			refs:    parts[i+2],
			author:  parts[i+3],
			date:    date,
			subject: strings.TrimRight(parts[i+5], "\r\n"),
			body:    strings.ReplaceAll(strings.TrimRight(parts[i+6], "\r\n"), "\r\n", "\n"),
		})
	}
	return commits, nil
}

// fileChange 表示一个 commit 中单个文件的变更。
type fileChange struct {
	status  string // git name-status，如 A、M、D、R100
	path    string // 变更后的路径
	oldPath string // 重命名/复制时的原路径
}

// gitCommitFiles 返回 commit 相对第一个 parent 的文件变更；根提交直接展示其内容。
func gitCommitFiles(repo string, commit commit) ([]fileChange, error) {
	var args []string
	if len(commit.parents) > 0 {
		args = []string{"diff", "--name-status", "-z", "-M", commit.parents[0], commit.hash}
	} else {
		args = []string{"show", "--format=", "--name-status", "-z", "-M", commit.hash}
	}
	output, err := gitOutputUntrimmed(repo, args...)
	if err != nil {
		return nil, fmt.Errorf("git commit files error: %w", err)
	}
	fields := strings.Split(output, "\x00")
	var changes []fileChange
	// 解析 git --name-status -z 的输出：
	// 每项为 status\0path\0，R/C 状态后跟 oldPath\0newPath\0。
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" || i >= len(fields) {
			continue
		}

		change := fileChange{status: status, path: fields[i]}
		i++
		if status[0] == 'R' || status[0] == 'C' {
			if i >= len(fields) {
				break
			}
			change.oldPath = change.path
			change.path = fields[i]
			i++
		}

		changes = append(changes, change)
	}
	return changes, nil
}

// gitFileDiff 返回单个文件在 commit 中相对第一个 parent 的 diff，并交给 git-delta 着色。
// delta 执行失败时返回未着色的原始 diff，调用方可以回退到 buildPlainDiff。
func gitFileDiff(repo string, commit commit, change fileChange) (string, error) {
	var args []string
	if len(commit.parents) > 0 {
		args = []string{"diff", "-M", "--no-color", "--no-ext-diff", commit.parents[0], commit.hash, "--"}
	} else {
		args = []string{"show", "--format=", "-M", "--no-color", "--no-ext-diff", commit.hash, "--"}
	}
	// 重命名/复制需要同时限制新旧路径，否则 git 会把新路径识别成新增文件
	if change.oldPath != "" {
		args = append(args, change.oldPath)
	}
	args = append(args, change.path)

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
