package tui

import (
	"lanegraph/internal/async"
	"lanegraph/internal/core"
)

type refsRequest struct{}

type commitsRequest struct{}

type filesRequest struct {
	commit  core.Commit
	compare *core.Commit
}

type diffRequest struct {
	commit  core.Commit
	change  core.FileChange
	compare *core.Commit
	options core.DiffOptions
}

type localFilesRequest struct{}

type localDiffRequest struct {
	change  core.FileChange
	options core.DiffOptions
}

type historyRequest struct {
	path string
}

type historyDiffRequest struct {
	commit  core.Commit
	change  core.FileChange
	options core.DiffOptions
}

type checkoutRequest struct {
	branch core.Branch
	tag    core.Tag
}

// name 返回 task key 和进行中文案使用的名字。
func (r checkoutRequest) name() string {
	if r.tag.Name != "" {
		return r.tag.Name
	}
	return r.branch.Name
}

// createOp 是新建引用操作类型。
type createOp uint8

const (
	opCreateBranch createOp = iota
	opCreateTag
)

type createRequest struct {
	op     createOp
	name   string
	commit *core.Commit // nil 表示从 HEAD 创建
}

// deleteRequest 是一次删除引用操作；force 仅对本地分支生效。
type deleteRequest struct {
	branch core.Branch
	tag    core.Tag
	force  bool
}

// name 返回 task key 和状态栏文案使用的名字。
func (r deleteRequest) name() string {
	if r.tag.Name != "" {
		return r.tag.Name
	}
	return r.branch.Name
}

// syncOp 是当前分支的远程同步操作类型。
type syncOp uint8

const (
	opPull syncOp = iota
	opPush
	opPushForce
	opFetch
)

type syncOpRequest struct {
	op syncOp
}

// commitOp 是 commit 列表上的操作类型。
type commitOp uint8

const (
	opReset commitOp = iota
	opRebase
	opRebaseInteractive
	opRebaseContinue
	opRebaseAbort
	opMerge
	opMergeContinue
	opMergeAbort
	opSquashContinue
	opSquashAbort
	opCommit
)

type commitOpRequest struct {
	op            commitOp
	commit        core.Commit
	resetMode     core.ResetMode               // 仅 reset 使用
	mergeMode     core.MergeMode               // 仅 merge 使用
	rebaseActions map[string]core.RebaseAction // 仅 interactive rebase 使用
	subject       string                       // 仅 commit 使用
	description   string                       // 仅 commit 使用
	amend         bool                         // 仅 commit 使用
}

// changeOp 是本地变更列表上的操作类型。
type changeOp uint8

const (
	opStage changeOp = iota
	opUnstage
	opDiscard
)

// changeOpRequest 是一次本地变更操作；stage/unstage 可针对目录下的多个文件，
// discard 只对单个文件生效（changes 长度为 1）。
type changeOpRequest struct {
	op      changeOp
	changes []core.FileChange
	path    string // 状态栏文案：单文件为文件路径，目录为目录路径
}

// tasks 汇总 TUI 使用的全部异步任务。
type tasks struct {
	refs    *async.Task[core.Refs, refsRequest]
	commits *async.Task[[]core.Commit, commitsRequest]

	diff  *async.Task[string, diffRequest]
	files *async.Task[[]core.FileChange, filesRequest]

	localDiff  *async.Task[string, localDiffRequest]
	localFiles *async.Task[[]core.FileChange, localFilesRequest]

	fileHistory     *async.Task[[]core.Commit, historyRequest]
	fileHistoryDiff *async.Task[string, historyDiffRequest]

	// 操作任务不缓存，每次 Execute 都会真正执行。
	checkout *async.Task[string, checkoutRequest]
	create   *async.Task[string, createRequest]
	delete   *async.Task[string, deleteRequest]
	sync     *async.Task[string, syncOpRequest]
	commit   *async.Task[string, commitOpRequest]
	change   *async.Task[string, changeOpRequest]
}

func (ts *tasks) lifecycles() []async.Lifecycle {
	return []async.Lifecycle{
		ts.refs,
		ts.commits,
		ts.diff,
		ts.files,
		ts.localDiff,
		ts.localFiles,
		ts.fileHistory,
		ts.fileHistoryDiff,
		ts.checkout,
		ts.create,
		ts.delete,
		ts.sync,
		ts.commit,
		ts.change,
	}
}

func (ts *tasks) wait() {
	for _, task := range ts.lifecycles() {
		task.Wait()
	}
}

func (ts *tasks) close() {
	for _, task := range ts.lifecycles() {
		task.Close()
	}
}
