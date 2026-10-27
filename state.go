package main

// state 是 TUI 和后续 GUI 共用的数据、缓存与会话状态。
type state struct {
	repo     string
	repoName string
	headRef  string
	headHash string

	commits  []commit
	branches []branch

	fileCache map[string][]fileChange
	diffCache map[string]string // 原始 diff（delta 的 ANSI 输出），由各 UI 自行着色

	currentCommit commit
}

func newState(repo string) (state, error) {
	commits, err := gitCommits(repo)
	if err != nil {
		return state{}, err
	}

	branches, err := gitBranches(repo)
	if err != nil {
		return state{}, err
	}

	headRef, headHash := gitHeadRef(repo)

	return state{
		repo:      repo,
		repoName:  gitRepoName(repo),
		headRef:   headRef,
		headHash:  headHash,
		commits:   commits,
		branches:  branches,
		fileCache: make(map[string][]fileChange),
		diffCache: make(map[string]string),
	}, nil
}

// files 带缓存地返回某个 commit 的文件变更列表。
func (s *state) files(commit commit) ([]fileChange, error) {
	if s.fileCache == nil {
		s.fileCache = make(map[string][]fileChange)
	}

	if changes, ok := s.fileCache[commit.hash]; ok {
		return changes, nil
	}

	changes, err := gitCommitFiles(s.repo, commit)
	if err != nil {
		return nil, err
	}
	s.fileCache[commit.hash] = changes
	return changes, nil
}

// diff 带缓存地返回单个文件的原始 diff，不包含任何着色标记。
// git-delta 不可用时返回原始 diff 和错误，由调用方决定如何展示。
func (s *state) diff(commit commit, change fileChange) (string, error) {
	if s.diffCache == nil {
		s.diffCache = make(map[string]string)
	}

	key := commit.hash + "\x00" + change.path
	if text, ok := s.diffCache[key]; ok {
		return text, nil
	}

	text, err := gitFileDiff(s.repo, commit, change)
	if err != nil {
		return text, err
	}
	s.diffCache[key] = text
	return text, nil
}
