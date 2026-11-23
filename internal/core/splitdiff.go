package core

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// splitDiffThemeName 是内嵌主题在 git-split-diffs 中的名字。
const splitDiffThemeName = "lanegraph"

// splitDiffTheme 基于 git-split-diffs 的 dark 主题，只调整背景：
// diff 行和词强调使用与 delta 一致的背景色，整体背景改为透明以跟随终端。
// 前景色、行号和语法主题保持 split 默认。
//
//go:embed splitdiff_theme.json
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

// colorizeSplitDiff 把原始 diff 交给 git-split-diffs 着色。
// git-split-diffs 按终端尺寸排版，这里用 COLUMNS/LINES 固定成面板宽度；
// 再用 min-line-width 强制布局与 side-by-side 开关一致：阈值 0 走左右对照，更大则走单栏。
// 主题通过 git 配置注入，只覆盖 diff 背景色，不修改用户自己的 split 配置。
func colorizeSplitDiff(ctx context.Context, diff string, options DiffOptions) (string, error) {
	width := options.Width
	if width <= 0 {
		width = 80
	}
	minLineWidth := 100000
	if options.SideBySide {
		minLineWidth = 0
	}

	config := []string{
		"split-diffs.min-line-width", fmt.Sprint(minLineWidth),
	}
	if dir := splitDiffThemeDir(); dir != "" {
		config = append(config,
			"split-diffs.theme-directory", dir,
			"split-diffs.theme-name", splitDiffThemeName,
		)
	}
	env := append(os.Environ(),
		"COLUMNS="+fmt.Sprint(width),
		"LINES=24",
		"GIT_CONFIG_COUNT="+fmt.Sprint(len(config)/2),
	)
	for i := 0; i < len(config); i += 2 {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i/2, config[i]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i/2, config[i+1]),
		)
	}

	cmd := exec.CommandContext(ctx, "git-split-diffs", "--color=truecolor")
	cmd.Stdin = strings.NewReader(diff)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git-split-diffs error: %w", err)
	}
	return string(output), nil
}
