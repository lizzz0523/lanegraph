package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// State 是 TUI 和后续 GUI 共用的数据与会话状态。
type State struct {
	Repo     string
	RepoName string
	HeadRef  string
	HeadHash string
	// Rebasing 表示仓库有未完成的 rebase（冲突解决中）。
	Rebasing bool
	// Merging 表示仓库有未完成的 merge（冲突解决中）。
	Merging bool
	// Squashing 表示 squash merge 已应用但尚未提交，通常是有冲突或自动提交失败。
	Squashing bool
	// CherryPicking 表示仓库有未完成的 cherry-pick（冲突解决中）。
	CherryPicking bool

	Branches []Branch
	Tags     []Tag

	Commits       []Commit
	CurrentCommit Commit
	CompareCommit *Commit

	// IgnoreWhitespace 表示 diff 是否忽略纯空白改动。
	IgnoreWhitespace bool
	// SideBySide 表示 diff 是否使用左右对照布局。
	SideBySide bool
	// Colorizers 是启动时探测到的可用 diff 着色器，按优先顺序排列。
	Colorizers []DiffColorizer
}

func NewState(ctx context.Context, repo string) (State, error) {
	repo = GitRoot(ctx, repo)
	s := State{Repo: repo, IgnoreWhitespace: true, Colorizers: detectDiffColorizers()}

	refs, err := s.Refs(ctx)
	if err != nil {
		return State{}, err
	}

	commits, err := s.History(ctx)
	if err != nil {
		return State{}, err
	}

	s.Branches = refs.Branches
	s.Tags = refs.Tags
	s.RepoName = filepath.Base(repo)
	s.HeadRef = refs.HeadRef
	s.HeadHash = refs.HeadHash
	s.Rebasing = refs.Rebasing
	s.Merging = refs.Merging
	s.Squashing = refs.Squashing
	s.CherryPicking = refs.CherryPicking
	s.Commits = commits
	return s, nil
}

// Refs 是仓库的分支、标签与 HEAD 快照。
type Refs struct {
	Branches      []Branch
	Tags          []Tag
	HeadRef       string
	HeadHash      string
	Rebasing      bool
	Merging       bool
	Squashing     bool
	CherryPicking bool
}

// Refs 返回仓库的分支、标签与 HEAD 快照。
func (s *State) Refs(ctx context.Context) (Refs, error) {
	branches, err := GitBranches(ctx, s.Repo)
	if err != nil {
		return Refs{}, err
	}
	tags, err := GitTags(ctx, s.Repo)
	if err != nil {
		return Refs{}, err
	}
	headRef, headHash := GitRepoHead(ctx, s.Repo)
	return Refs{
		Branches:      branches,
		Tags:          tags,
		HeadRef:       headRef,
		HeadHash:      headHash,
		Rebasing:      GitRebaseInProgress(ctx, s.Repo),
		Merging:       GitMergeInProgress(ctx, s.Repo),
		Squashing:     GitSquashInProgress(ctx, s.Repo),
		CherryPicking: GitCherryPickInProgress(ctx, s.Repo),
	}, nil
}

// CheckoutBranch 切换分支并返回实际检出的本地分支名。
func (s *State) CheckoutBranch(ctx context.Context, branch Branch) (string, error) {
	return GitCheckoutBranch(ctx, s.Repo, branch)
}

// Checkout 检出标签，HEAD 进入 detached 状态。
func (s *State) Checkout(ctx context.Context, tag Tag) (string, error) {
	return GitCheckout(ctx, s.Repo, tag.Name)
}

// CreateBranch 从 commit 创建并检出分支；commit 为 nil 时从 HEAD 创建。
func (s *State) CreateBranch(ctx context.Context, name string, commit *Commit) (string, error) {
	startPoint := ""
	if commit != nil {
		startPoint = commit.Hash
	}
	return GitCreateBranch(ctx, s.Repo, name, startPoint)
}

// CreateTag 从 commit 创建轻量标签；commit 为 nil 时从 HEAD 创建。
func (s *State) CreateTag(ctx context.Context, name string, commit *Commit) (string, error) {
	startPoint := ""
	if commit != nil {
		startPoint = commit.Hash
	}
	return GitCreateTag(ctx, s.Repo, name, startPoint)
}

// DeleteBranch 删除本地分支；force 为 true 时删除未合并的分支。
func (s *State) DeleteBranch(ctx context.Context, name string, force bool) error {
	return GitDeleteBranch(ctx, s.Repo, name, force)
}

// DeleteRemoteBranch 在远端删除分支。
func (s *State) DeleteRemoteBranch(ctx context.Context, branch Branch) error {
	return GitDeleteRemoteBranch(ctx, s.Repo, branch)
}

// DeleteTag 删除标签。
func (s *State) DeleteTag(ctx context.Context, name string) error {
	return GitDeleteTag(ctx, s.Repo, name)
}

// History 返回仓库的提交历史。
func (s *State) History(ctx context.Context) ([]Commit, error) {
	return GitCommits(ctx, s.Repo)
}

// FileHistory 返回某个文件的提交历史。
func (s *State) FileHistory(ctx context.Context, path string) ([]Commit, error) {
	return GitFileHistory(ctx, s.Repo, path)
}

// Pull 拉取当前分支的上游变更。
func (s *State) Pull(ctx context.Context) (string, error) {
	return GitPull(ctx, s.Repo)
}

// Push 推送当前分支到它的上游远端；force 为 true 时使用 --force-with-lease，
// track 为 true 时把远端分支设为当前分支的 upstream。
func (s *State) Push(ctx context.Context, force, track bool) (string, error) {
	return GitPush(ctx, s.Repo, force, track)
}

// Fetch 拉取远端引用。
func (s *State) Fetch(ctx context.Context) (string, error) {
	return GitFetch(ctx, s.Repo)
}

// Reset 把当前分支重置到 commit。
func (s *State) Reset(ctx context.Context, commit Commit, mode ResetMode) (string, error) {
	return GitReset(ctx, s.Repo, commit, mode)
}

// Rebase 把当前分支 rebase 到 commit。
func (s *State) Rebase(ctx context.Context, commit Commit) (string, error) {
	return GitRebase(ctx, s.Repo, commit)
}

// RebaseInteractive 以 commit 为基点执行 interactive rebase，actions 指定每个提交的动作。
func (s *State) RebaseInteractive(ctx context.Context, commit Commit, actions map[string]RebaseAction) (string, error) {
	return GitRebaseInteractive(ctx, s.Repo, commit, actions)
}

// Reword 改写 commit 的提交信息。
func (s *State) Reword(ctx context.Context, commit Commit, subject, description string) (string, error) {
	return GitReword(ctx, s.Repo, commit, subject, description)
}

// RebaseContinue 在冲突解决并暂存后继续 rebase。
func (s *State) RebaseContinue(ctx context.Context) (string, error) {
	return GitRebaseContinue(ctx, s.Repo)
}

// RebaseAbort 取消未完成的 rebase。
func (s *State) RebaseAbort(ctx context.Context) (string, error) {
	return GitRebaseAbort(ctx, s.Repo)
}

// Merge 把 commit 按 mode 合入当前分支。
func (s *State) Merge(ctx context.Context, commit Commit, mode MergeMode) (string, error) {
	return GitMerge(ctx, s.Repo, commit, mode)
}

// MergeContinue 在冲突解决并暂存后继续 merge。
func (s *State) MergeContinue(ctx context.Context) (string, error) {
	return GitMergeContinue(ctx, s.Repo)
}

// MergeAbort 取消未完成的 merge。
func (s *State) MergeAbort(ctx context.Context) (string, error) {
	return GitMergeAbort(ctx, s.Repo)
}

// SquashContinue 提交待完成的 squash。
func (s *State) SquashContinue(ctx context.Context) (string, error) {
	return GitSquashContinue(ctx, s.Repo)
}

// SquashAbort 取消待完成的 squash。
func (s *State) SquashAbort(ctx context.Context) (string, error) {
	return GitSquashAbort(ctx, s.Repo)
}

// CherryPick 把 commit 应用到当前分支。
func (s *State) CherryPick(ctx context.Context, commit Commit) (string, error) {
	return GitCherryPick(ctx, s.Repo, commit)
}

// CherryPickContinue 在冲突解决并暂存后继续 cherry-pick。
func (s *State) CherryPickContinue(ctx context.Context) (string, error) {
	return GitCherryPickContinue(ctx, s.Repo)
}

// CherryPickAbort 取消未完成的 cherry-pick。
func (s *State) CherryPickAbort(ctx context.Context) (string, error) {
	return GitCherryPickAbort(ctx, s.Repo)
}

// Commit 用 subject 和可选的 description 提交暂存区改动；amend 为 true 时改写 HEAD 提交。
func (s *State) Commit(ctx context.Context, subject, description string, amend bool) (string, error) {
	return GitCommit(ctx, s.Repo, subject, description, amend)
}

// Files 返回某个 commit 相对 compare 的文件变更列表；compare 为 nil 时相对第一个 parent。
func (s *State) Files(ctx context.Context, commit Commit, compare *Commit) ([]FileChange, error) {
	return GitCommitFiles(ctx, s.Repo, commit, compare)
}

// Diff 返回单个文件的 diff，通常是着色后的 ANSI 输出。
// compare 为 nil 时相对第一个 parent；options 控制空白忽略、布局和宽度。
// 着色器都不可用时返回原始 diff 和错误，由调用方决定如何展示。
func (s *State) Diff(ctx context.Context, commit Commit, compare *Commit, change FileChange, options DiffOptions) (string, error) {
	return GitFileDiff(ctx, s.Repo, commit, compare, change, options)
}

// LocalFiles 返回暂存区、未暂存和未跟踪的全部本地变更。
func (s *State) LocalFiles(ctx context.Context) ([]FileChange, error) {
	return GitLocalChanges(ctx, s.Repo)
}

// LocalDiff 返回本地单个文件的 diff，通常是着色后的 ANSI 输出。
// options 控制空白忽略、布局和宽度。
func (s *State) LocalDiff(ctx context.Context, change FileChange, options DiffOptions) (string, error) {
	return GitLocalFileDiff(ctx, s.Repo, change, options)
}

// Stage 把变更加入暂存区。
func (s *State) Stage(ctx context.Context, changes []FileChange) error {
	return GitStage(ctx, s.Repo, changes)
}

// Unstage 把变更移回工作区。
func (s *State) Unstage(ctx context.Context, changes []FileChange) error {
	return GitUnstage(ctx, s.Repo, changes)
}

// Discard 丢弃单个文件的改动。
func (s *State) Discard(ctx context.Context, change FileChange) error {
	return GitDiscard(ctx, s.Repo, change)
}

// OpenFile 使用系统默认应用打开仓库内的文件（仅支持 macOS）。
// 文件不存在或不是普通文件时直接返回错误，避免把目录交给 open。
func (s *State) OpenFile(path string) error {
	abs, err := filepath.Abs(filepath.Join(s.Repo, path))
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}

	cmd := exec.Command("open", abs)
	if err := cmd.Start(); err != nil {
		return err
	}
	// open 会立即交给 LaunchServices；单独回收进程，避免阻塞事件循环或留下僵尸进程。
	go cmd.Wait()
	return nil
}
