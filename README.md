# lanegraph

在终端里以图形方式浏览 git 提交历史的 TUI 工具。

![lanegraph](screenshot.png)

## 安装

需要 Go 1.24 或以上版本：

```sh
go install github.com/lizzz0523/lanegraph@latest
```

也可以克隆仓库后本地安装：

```sh
git clone https://github.com/lizzz0523/lanegraph.git
cd lanegraph
go install .
```

安装后二进制位于 `$GOBIN`（未设置时为 `$(go env GOPATH)/bin`），确保该目录在 `PATH` 中。

### 更好的 diff 显示

lanegraph 会自动探测 `PATH` 中的 diff 着色器，优先使用 `git-split-diffs`，其次使用 `git-delta`，安装任一即可生效：

```sh
npm install -g git-split-diffs   # 或
brew install git-delta
```

未安装时使用内置的基础着色。

## 使用

```sh
lanegraph            # 查看当前目录所在仓库
lanegraph /path/repo # 查看指定仓库
```

按 `q` 退出。
