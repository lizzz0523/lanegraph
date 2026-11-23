package core

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// colorizeDeltaDiff 把原始 diff 交给 git-delta 着色。
// side-by-side 使用固定宽度（面板内宽）并无限折行以完整展示内容，其余情况保持 variable 宽度。
func colorizeDeltaDiff(ctx context.Context, diff string, options DiffOptions) (string, error) {
	args := []string{
		"--paging=never", "--no-gitconfig", "--true-color=always",
		"--line-numbers", "--file-style=omit",
		"--hunk-header-decoration-style=gray box",
		"--hunk-header-line-number-style=gray",
		"--whitespace-error-style=magenta dim",
		"--line-numbers-left-style=gray",
		"--line-numbers-right-style=gray",
		"--line-numbers-zero-style=gray",
		"--inline-hint-style=gray",
		// 压暗 +/− 行的整行底色；强调片段仍由 delta 的 emph 样式自动提亮
		"--minus-style=syntax \"#3b2323\"",
		"--plus-style=syntax \"#20352a\"",
	}
	if options.SideBySide {
		width := options.Width
		if width <= 0 {
			width = 80
		}
		args = append(args,
			"--side-by-side",
			fmt.Sprintf("--width=%d", width),
			"--wrap-max-lines=unlimited",
		)
	} else {
		args = append(args, "--width=variable")
	}

	cmd := exec.CommandContext(ctx, "delta", args...)
	cmd.Stdin = strings.NewReader(diff)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git-delta error: %w", err)
	}
	return string(output), nil
}
