package core

// State 是 TUI 和后续 GUI 共用的数据、缓存与会话状态。
type State struct {
	Repo     string
	RepoName string
	HeadRef  string
	HeadHash string

	Commits  []Commit
	Branches []Branch

	fileCache map[string][]FileChange
	diffCache map[string]string // 原始 diff（delta 的 ANSI 输出），由各 UI 自行着色

	CurrentCommit Commit
}

func NewState(repo string) (State, error) {
	commits, err := GitCommits(repo)
	if err != nil {
		return State{}, err
	}

	branches, err := GitBranches(repo)
	if err != nil {
		return State{}, err
	}

	headRef, headHash := GitRepoHead(repo)

	return State{
		Repo:      repo,
		RepoName:  GitRepoName(repo),
		HeadRef:   headRef,
		HeadHash:  headHash,
		Commits:   commits,
		Branches:  branches,
		fileCache: make(map[string][]FileChange),
		diffCache: make(map[string]string),
	}, nil
}

// Files 带缓存地返回某个 commit 的文件变更列表。
func (s *State) Files(commit Commit) ([]FileChange, error) {
	if s.fileCache == nil {
		s.fileCache = make(map[string][]FileChange)
	}

	if changes, ok := s.fileCache[commit.Hash]; ok {
		return changes, nil
	}

	changes, err := GitCommitFiles(s.Repo, commit)
	if err != nil {
		return nil, err
	}
	s.fileCache[commit.Hash] = changes
	return changes, nil
}

// Diff 带缓存地返回单个文件的原始 diff，不包含任何着色标记。
// git-delta 不可用时返回原始 diff 和错误，由调用方决定如何展示。
func (s *State) Diff(commit Commit, change FileChange) (string, error) {
	if s.diffCache == nil {
		s.diffCache = make(map[string]string)
	}

	key := commit.Hash + "\x00" + change.Path
	if text, ok := s.diffCache[key]; ok {
		return text, nil
	}

	text, err := GitFileDiff(s.Repo, commit, change)
	if err != nil {
		return text, err
	}
	s.diffCache[key] = text
	return text, nil
}
