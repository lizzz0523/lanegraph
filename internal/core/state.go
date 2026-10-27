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

func NewState(ctx context.Context, repo string) (State, error) {
	commits, err := GitCommits(ctx, repo)
	if err != nil {
		return State{}, err
	}

	branches, err := GitBranches(ctx, repo)
	if err != nil {
		return State{}, err
	}

	headRef, headHash := GitRepoHead(ctx, repo)

	return State{
		Repo:     repo,
		RepoName: GitRepoName(ctx, repo),
		HeadRef:  headRef,
		HeadHash: headHash,
		Commits:  commits,
		Branches: branches,
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
