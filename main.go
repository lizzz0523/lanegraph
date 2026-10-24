package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

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

	c := &client{
		commits:  commits,
		branches: branches,
	}
	if err := c.run(); err != nil {
		log.Fatal(err)
	}
}

type client struct {
	app *tview.Application

	branches   []branch
	branchList *tview.TreeView

	commits    []commit
	commitList *laneView

	infoWidth  int
	commitInfo *tview.TextView

	fileList *tview.TreeView
	fileDiff *diffView
}

type branch struct {
	name   string
	hash   string
	remote bool
}

func gitBranches(repo string) ([]branch, error) {
	// 本地 + 远端分支；按提交时间倒序，最新改动的排在前面
	cmd := exec.Command("git", "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)",
		"refs/heads", "refs/remotes")
	cmd.Dir = repo

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref error: %w", err)
	}

	var branches []branch
	for line := range strings.Lines(string(output)) {
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
	cmd := exec.Command("git", "log", "-z", "--date-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%an%x00%ct%x00%s%x00%b")
	cmd.Dir = repo

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	// 字段和记录都以 NUL 分隔，每 7 段是一个 commit；末尾终止符会多出一个空段
	parts := strings.Split(string(output), "\x00")
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
