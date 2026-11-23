package core

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// splitDiffThemeName 是内嵌主题在 git-split-diffs 中的名字。
const splitDiffThemeName = "lanegraph"

// hunk 标题中旧/新范围的强调色，与主题里删除行、新增行的前景色一致。
const (
	splitDiffDeletedFg  = "\x1b[38;2;204;102;102m"
	splitDiffInsertedFg = "\x1b[38;2;102;204;102m"
)

// splitDiffHunkHeader 匹配 git-split-diffs 的 hunk 标题首行：
// 样式包裹的 @@ -old +new @@ context；分组依次是样式前缀、旧范围、新范围、上下文。
var splitDiffHunkHeader = regexp.MustCompile(`^((?:\x1b\[2m)?\x1b\[[0-9;]*m)@@ (-[^ ]+) (\+[^ ]+)(?: @@)?(.*)\x1b\[39m(?:\x1b\[22m)?$`)

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
	return rewriteSplitDiffOutput(string(output)), nil
}

// rewriteSplitDiffOutput 调整 git-split-diffs 的输出：
// 去掉自带的文件标题行（面板标题已经显示路径），
// 并把每个 hunk 标题改成整行带分隔线的样式，范围部分分别用红/绿着色。
func rewriteSplitDiffOutput(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(output, "\n")

	hasHunk := false
	for _, line := range lines {
		if splitDiffHunkHeader.MatchString(line) {
			hasHunk = true
			break
		}
	}
	// 二进制 diff 只有文件标题，删掉会让面板变成空白。
	if !hasHunk {
		return output
	}

	out := make([]string, 0, len(lines))
	divider := ""
	for i := 0; i < len(lines); i++ {
		switch {
		case i+2 < len(lines) && strings.Count(lines[i+1], "■") >= 2:
			// 文件标题块：分隔线 + ■■ path + 分隔线。
			divider = lines[i]
			i += 2
		case splitDiffHunkHeader.MatchString(lines[i]):
			match := splitDiffHunkHeader.FindStringSubmatch(lines[i])
			if divider != "" {
				out = append(out, divider)
			}
			out = append(out, rewriteSplitDiffHunkHeader(match))
			// 窄面板下 hunk 标题会折行，续行保持原样。
			for i++; i < len(lines) && strings.HasPrefix(lines[i], match[1]); i++ {
				out = append(out, lines[i])
			}
			i--
			if divider != "" {
				out = append(out, divider)
			}
		default:
			out = append(out, lines[i])
		}
	}
	return strings.Join(out, "\n")
}

// rewriteSplitDiffHunkHeader 去掉 hunk 标题首行的 @@，
// 并把旧范围和新范围分别着色为红、绿；其余部分保持标题原本的文字颜色。
func rewriteSplitDiffHunkHeader(match []string) string {
	var b strings.Builder
	b.WriteString(match[1])
	b.WriteString(splitDiffDeletedFg)
	b.WriteString(match[2])
	b.WriteString(match[1])
	b.WriteString(" ")
	b.WriteString(splitDiffInsertedFg)
	b.WriteString(match[3])
	b.WriteString(match[1])
	b.WriteString(match[4])
	b.WriteString("\x1b[39m")
	return b.String()
}
