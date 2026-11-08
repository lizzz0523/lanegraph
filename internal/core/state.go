package core

import "context"

// State 是 TUI 和后续 GUI 共用的数据与会话状态。
type State struct {
	Repo     string
	RepoName string
	HeadRef  string
	HeadHash string

	Commits  []Commit
	Branches []Branch

	CurrentCommit Commit
	CompareCommit *Commit

	// IgnoreWhitespace 表示 diff 是否忽略纯空白改动。
	IgnoreWhitespace bool
	// SideBySide 表示 diff 是否使用左右对照布局。
	SideBySide bool
}

func NewState(ctx context.Context, repo string) (State, error) {
	s := State{Repo: repo, IgnoreWhitespace: true}

	refs, err := s.Refs(ctx)
	if err != nil {
		return State{}, err
	}

	commits, err := s.History(ctx)
	if err != nil {
		return State{}, err
	}

	s.RepoName = GitRepoName(ctx, repo)
	s.HeadRef = refs.HeadRef
	s.HeadHash = refs.HeadHash
	s.Branches = refs.Branches
	s.Commits = commits
	return s, nil
}

// History 返回仓库的提交历史。
func (s *State) History(ctx context.Context) ([]Commit, error) {
	return GitCommits(ctx, s.Repo)
}

// Refs 是仓库的分支与 HEAD 快照。
type Refs struct {
	Branches []Branch
	HeadRef  string
	HeadHash string
}

// Refs 返回仓库的分支与 HEAD 快照。
func (s *State) Refs(ctx context.Context) (Refs, error) {
	branches, err := GitBranches(ctx, s.Repo)
	if err != nil {
		return Refs{}, err
	}
	headRef, headHash := GitRepoHead(ctx, s.Repo)
	return Refs{
		Branches: branches,
		HeadRef:  headRef,
		HeadHash: headHash,
	}, nil
}

// Files 返回某个 commit 相对 compare 的文件变更列表；compare 为 nil 时相对第一个 parent。
func (s *State) Files(ctx context.Context, commit Commit, compare *Commit) ([]FileChange, error) {
	return GitCommitFiles(ctx, s.Repo, commit, compare)
}

// Diff 返回单个文件的 diff，通常是 git-delta 的 ANSI 输出。
// compare 为 nil 时相对第一个 parent；options 控制空白忽略和左右对照布局。
// git-delta 不可用时返回原始 diff 和错误，由调用方决定如何展示。
func (s *State) Diff(ctx context.Context, commit Commit, compare *Commit, change FileChange, options DiffOptions) (string, error) {
	return GitFileDiff(ctx, s.Repo, commit, compare, change, options)
}

// LocalFiles 返回暂存区、未暂存和未跟踪的全部本地变更。
func (s *State) LocalFiles(ctx context.Context) ([]FileChange, error) {
	return GitLocalChanges(ctx, s.Repo)
}

// LocalDiff 返回本地单个文件的 diff，通常是 git-delta 的 ANSI 输出。
// options 控制空白忽略和左右对照布局。
func (s *State) LocalDiff(ctx context.Context, change FileChange, options DiffOptions) (string, error) {
	return GitLocalFileDiff(ctx, s.Repo, change, options)
}

// Stage 把单个文件变更加入暂存区。
func (s *State) Stage(ctx context.Context, change FileChange) error {
	return GitStage(ctx, s.Repo, change)
}

// Unstage 把单个文件变更移回工作区。
func (s *State) Unstage(ctx context.Context, change FileChange) error {
	return GitUnstage(ctx, s.Repo, change)
}

// Discard 丢弃单个文件的改动。
func (s *State) Discard(ctx context.Context, change FileChange) error {
	return GitDiscard(ctx, s.Repo, change)
}

// Checkout 切换分支并返回实际检出的本地分支名。
func (s *State) Checkout(ctx context.Context, branch Branch) (string, error) {
	return GitCheckout(ctx, s.Repo, branch)
}
