package main

import (
	"log"
	"os"

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
