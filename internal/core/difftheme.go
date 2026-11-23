package core

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
)

// splitDiffThemeName 是内嵌主题在 git-split-diffs 中的名字。
const splitDiffThemeName = "lanegraph"

// splitDiffTheme 基于 git-split-diffs 的 dark 主题，只调整背景：
// diff 行和词强调使用与 delta 一致的背景色，整体背景改为透明以跟随终端。
// 前景色、行号和语法主题保持 split 默认。
//
//go:embed difftheme.json
var splitDiffTheme string

var (
	splitDiffThemeOnce    sync.Once
	splitDiffThemeDirPath string
)

// splitDiffThemeDir 把内嵌主题写到缓存目录并返回主题目录；
// 写入失败时返回空字符串，调用方退回 git-split-diffs 的默认主题。
func splitDiffThemeDir() string {
	splitDiffThemeOnce.Do(func() {
		cache, err := os.UserCacheDir()
		if err != nil {
			return
		}
		dir := filepath.Join(cache, "lanegraph", "split-diffs")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}

		// 先写临时文件再改名，避免另一个实例读到写了一半的主题。
		tmp, err := os.CreateTemp(dir, "theme-*.json")
		if err != nil {
			return
		}
		if _, err := tmp.WriteString(splitDiffTheme); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return
		}
		if err := os.Rename(tmp.Name(), filepath.Join(dir, splitDiffThemeName+".json")); err != nil {
			os.Remove(tmp.Name())
			return
		}
		splitDiffThemeDirPath = dir
	})
	return splitDiffThemeDirPath
}
