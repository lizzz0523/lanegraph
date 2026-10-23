package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
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

	tui(branches, commits)
}

type commit struct {
	hash    string
	parents []string
	refs    string // %D，如 "HEAD -> main, origin/main, tag: v1"
	subject string
}

func gitCommits(repo string) ([]commit, error) {
	// 起点为 HEAD + 本地分支 + 远端分支 + 标签（不含 stash），未合并的分支也会被画出来
	cmd := exec.Command("git", "log", "--date-order", "--branches",
		"--remotes", "--tags", "HEAD", "--format=%H%x00%P%x00%D%x00%s")
	cmd.Dir = repo

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log error: %w", err)
	}

	var commits []commit
	for line := range strings.Lines(string(output)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, commit{
			hash:    parts[0],
			parents: strings.Fields(parts[1]),
			refs:    parts[2],
			subject: parts[3],
		})
	}
	return commits, nil
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
