package main

import (
	"fmt"
	"strconv"
	"strings"
)

type ansiStyle struct {
	fg, bg             string
	bold, dim, italic  bool
	underline, reverse bool
}

// tag 输出完整样式；未开启的属性用大写字母显式清除，避免样式延续到后续文本。
func (s *ansiStyle) tag() string {
	fg, bg := s.fg, s.bg
	if fg == "" {
		fg = "-"
	}
	if bg == "" {
		bg = "-"
	}

	var attrs [5]byte
	n := 0
	if s.bold {
		attrs[n] = 'b'
	} else {
		attrs[n] = 'B'
	}
	n++
	if s.dim {
		attrs[n] = 'd'
	} else {
		attrs[n] = 'D'
	}
	n++
	if s.italic {
		attrs[n] = 'i'
	} else {
		attrs[n] = 'I'
	}
	n++
	if s.underline {
		attrs[n] = 'u'
	} else {
		attrs[n] = 'U'
	}
	n++
	if s.reverse {
		attrs[n] = 'r'
	} else {
		attrs[n] = 'R'
	}
	n++

	return "[" + fg + ":" + bg + ":" + string(attrs[:n]) + "]"
}

// consumeEscape 返回消耗的字节数；SGR 会就地更新 style。
func consumeEscape(s string, style *ansiStyle) int {
	if len(s) < 2 {
		return len(s)
	}

	switch s[1] {
	case '[':
		end := 2
		for end < len(s) && (s[end] < 0x40 || s[end] > 0x7e) {
			end++
		}
		if end >= len(s) {
			return len(s)
		}
		if s[end] == 'm' {
			applySGR(s[2:end], style)
		}
		return end + 1
	case ']':
		// OSC（例如超链接）：以 BEL 或 ST 结尾
		for end := 2; end < len(s); end++ {
			if s[end] == '\a' {
				return end + 1
			}
			if s[end] == '\x1b' && end+1 < len(s) && s[end+1] == '\\' {
				return end + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}

func applySGR(params string, style *ansiStyle) {
	if params == "" {
		*style = ansiStyle{}
		return
	}

	fields := strings.Split(params, ";")
	nums := make([]int, len(fields))
	for i, field := range fields {
		if field != "" {
			nums[i], _ = strconv.Atoi(field)
		}
	}

	for i := 0; i < len(nums); i++ {
		switch n := nums[i]; {
		case n == 0:
			*style = ansiStyle{}
		case n == 1:
			style.bold = true
		case n == 2:
			style.dim = true
		case n == 3:
			style.italic = true
		case n == 4:
			style.underline = true
		case n == 7:
			style.reverse = true
		case n == 21 || n == 22:
			style.bold = false
			style.dim = false
		case n == 23:
			style.italic = false
		case n == 24:
			style.underline = false
		case n == 27:
			style.reverse = false
		case n >= 30 && n <= 37:
			style.fg = paletteHex(n - 30)
		case n == 38:
			if i+2 < len(nums) && nums[i+1] == 5 {
				style.fg = paletteHex(nums[i+2])
				i += 2
			} else if i+4 < len(nums) && nums[i+1] == 2 {
				style.fg = rgbHex(nums[i+2], nums[i+3], nums[i+4])
				i += 4
			}
		case n == 39:
			style.fg = ""
		case n >= 40 && n <= 47:
			style.bg = paletteHex(n - 40)
		case n == 48:
			if i+2 < len(nums) && nums[i+1] == 5 {
				style.bg = paletteHex(nums[i+2])
				i += 2
			} else if i+4 < len(nums) && nums[i+1] == 2 {
				style.bg = rgbHex(nums[i+2], nums[i+3], nums[i+4])
				i += 4
			}
		case n == 49:
			style.bg = ""
		case n >= 90 && n <= 97:
			style.fg = paletteHex(n - 90 + 8)
		case n >= 100 && n <= 107:
			style.bg = paletteHex(n - 100 + 8)
		}
	}
}

var ansiPalette = [16]string{
	"#000000", "#800000", "#008000", "#808000",
	"#000080", "#800080", "#008080", "#c0c0c0",
	"#808080", "#ff0000", "#00ff00", "#ffff00",
	"#0000ff", "#ff00ff", "#00ffff", "#ffffff",
}

var colorCube = [6]int{0, 95, 135, 175, 215, 255}

func paletteHex(index int) string {
	switch {
	case index < 16:
		return ansiPalette[index]
	case index < 232:
		index -= 16
		return rgbHex(colorCube[index/36], colorCube[(index/6)%6], colorCube[index%6])
	default:
		v := min(8+(index-232)*10, 255)
		return rgbHex(v, v, v)
	}
}

func rgbHex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", clampByte(r), clampByte(g), clampByte(b))
}

func clampByte(v int) int {
	return min(max(v, 0), 255)
}
