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
}

// Refs 是仓库的分支与 HEAD 快照。
type Refs struct {
	Branches []Branch
	HeadRef  string
	HeadHash string
}

func NewState(ctx context.Context, repo string) (State, error) {
	s := State{Repo: repo}

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

// Files 返回某个 commit 的文件变更列表。
func (s *State) Files(ctx context.Context, commit Commit) ([]FileChange, error) {
	return GitCommitFiles(ctx, s.Repo, commit)
}

// Diff 返回单个文件的 diff，通常是 git-delta 的 ANSI 输出。
// git-delta 不可用时返回原始 diff 和错误，由调用方决定如何展示。
func (s *State) Diff(ctx context.Context, commit Commit, change FileChange) (string, error) {
	return GitFileDiff(ctx, s.Repo, commit, change)
}

// LocalChanges 返回暂存区、未暂存和未跟踪的全部本地变更。
func (s *State) LocalChanges(ctx context.Context) ([]FileChange, error) {
	return GitLocalChanges(ctx, s.Repo)
}

// LocalDiff 返回本地单个文件的 diff，通常是 git-delta 的 ANSI 输出。
func (s *State) LocalDiff(ctx context.Context, change FileChange) (string, error) {
	return GitLocalFileDiff(ctx, s.Repo, change)
}

// Checkout 是 checkout 后重新读取的 refs 快照。
type Checkout struct {
	Branch   string // 实际检出的本地分支名
	Branches []Branch
	HeadRef  string
	HeadHash string
}

// Checkout 切换分支，并在后台重新读取分支列表与 HEAD 信息。
// 返回的快照需由调用方在 UI 线程用 ApplyCheckout 写回。
func (s *State) Checkout(ctx context.Context, branch Branch) (Checkout, error) {
	name, err := GitCheckout(ctx, s.Repo, branch)
	if err != nil {
		return Checkout{}, err
	}

	// 必须先 checkout 再读 refs，否则拿到的是切换前的分支列表。
	branches, err := GitBranches(ctx, s.Repo)
	if err != nil {
		return Checkout{}, err
	}
	headRef, headHash := GitRepoHead(ctx, s.Repo)
	return Checkout{
		Branch:   name,
		Branches: branches,
		HeadRef:  headRef,
		HeadHash: headHash,
	}, nil
}

// ApplyCheckout 把 checkout 结果写回 State。只能在 UI 线程调用。
func (s *State) ApplyCheckout(result Checkout) {
	s.Branches = result.Branches
	s.HeadRef = result.HeadRef
	s.HeadHash = result.HeadHash
}
