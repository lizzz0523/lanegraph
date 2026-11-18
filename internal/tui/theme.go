package tui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// 本文件只定义颜色和颜色标记，不包含尺寸和控件样式。
// themeColor 同时保存 tview markup 标签和 tcell 颜色：
// tag 用于文本内容，cell 用于控件样式。
type themeColor struct {
	tag  string
	cell tcell.Color
}

// 重置前景色、背景色和属性。
const colorReset = "[-::-]"

// markup 用主题色包裹文本；bold 为 true 时附加粗体。
func (c themeColor) markup(text string, bold ...bool) string {
	tag := c.tag
	if len(bold) > 0 && bold[0] {
		tag += "[::b]"
	}
	return tag + text + colorReset
}

// hexColor 从 0xRRGGBB 构造主题色，tag 和 cell 使用同一个颜色。
func hexColor(rgb int32) themeColor {
	return themeColor{
		tag:  fmt.Sprintf("[#%06x]", rgb),
		cell: tcell.NewHexColor(rgb),
	}
}

var (
	// 文本语义色
	colorMuted     = themeColor{tag: "[gray]", cell: tcell.ColorGray}
	colorSecondary = themeColor{tag: "[yellow]", cell: tcell.ColorYellow}
	colorAccent    = themeColor{tag: "[aqua]", cell: tcell.ColorAqua}
	colorSuccess   = themeColor{tag: "[green]", cell: tcell.ColorGreen}
	colorDanger    = themeColor{tag: "[red]", cell: tcell.ColorIndianRed}
	colorInfo      = themeColor{tag: "[blue]", cell: tcell.ColorBlue}
	colorSpecial   = themeColor{tag: "[fuchsia]", cell: tcell.ColorFuchsia}

	// 控件颜色
	colorText        = themeColor{cell: tview.Styles.PrimaryTextColor}
	colorBackground  = themeColor{cell: tcell.ColorDefault}
	colorBorder      = themeColor{cell: tcell.ColorDarkGray}
	colorPopup       = themeColor{cell: tcell.NewHexColor(0x34373d)}
	colorSelection   = themeColor{cell: tcell.NewHexColor(0x4a4a4a)}
	colorHighlight   = themeColor{cell: tcell.NewHexColor(0x2f4f4f)}
	colorOnAccent    = themeColor{cell: tcell.ColorBlack}
	colorTransparent = themeColor{cell: tcell.ColorNone}
)
