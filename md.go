package main

import (
	"html"
	"regexp"
	"strings"
)

// 只做本机预览需要的那部分 Markdown：标题 / 围栏代码 / 列表 / 引用 / 分隔线 /
// 行内 code·粗体·斜体·链接·图片。表格与脚注不做——降级即"原样当文本段输出"。

var (
	reImg   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	reLink  = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reBold  = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	reItalc = regexp.MustCompile(`\*([^*\n]+)\*|_([^_\n]+)_`)
	reCode  = regexp.MustCompile("`([^`]+)`")
	reHead  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reUL    = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	reOL    = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	reHR    = regexp.MustCompile(`^\s*(?:-\s*){3,}$|^\s*(?:\*\s*){3,}$|^\s*(?:_\s*){3,}$`)
)

func renderMarkdown(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	// YAML frontmatter（skill / obsidian 的 md 开头常见）直接丢掉，不当正文渲染
	if len(lines) > 1 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines) && i < 200; i++ {
			if t := strings.TrimSpace(lines[i]); t == "---" || t == "..." {
				lines = lines[i+1:]
				break
			}
		}
	}

	var sb strings.Builder
	var para []string
	listKind := "" // "ul" | "ol"

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		sb.WriteString("<p>" + inlineMD(strings.Join(para, "\n")) + "</p>\n")
		para = para[:0]
	}
	closeList := func() {
		if listKind != "" {
			sb.WriteString("</" + listKind + ">\n")
			listKind = ""
		}
	}
	openList := func(kind string) {
		if listKind == kind {
			return
		}
		closeList()
		sb.WriteString("<" + kind + ">\n")
		listKind = kind
	}

	inFence := false
	var fence strings.Builder
	fenceLang := ""

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")

		// 围栏代码块：内部一个字节都不解析
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			if inFence {
				lang := ""
				if fenceLang != "" {
					lang = ` class="lang-` + html.EscapeString(fenceLang) + `"`
				}
				sb.WriteString("<pre><code" + lang + ">" + html.EscapeString(fence.String()) + "</code></pre>\n")
				fence.Reset()
				fenceLang = ""
				inFence = false
			} else {
				flushPara()
				closeList()
				inFence = true
				fenceLang = strings.TrimSpace(line[3:])
			}
			continue
		}
		if inFence {
			fence.WriteString(raw)
			fence.WriteByte('\n')
			continue
		}

		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flushPara()
			closeList()
			continue

		case reHR.MatchString(trimmed):
			flushPara()
			closeList()
			sb.WriteString("<hr>\n")

		case reHead.MatchString(trimmed):
			flushPara()
			closeList()
			m := reHead.FindStringSubmatch(trimmed)
			n := len(m[1])
			sb.WriteString("<h" + itoa(n) + ">" + inlineMD(m[2]) + "</h" + itoa(n) + ">\n")

		case strings.HasPrefix(trimmed, ">"):
			flushPara()
			closeList()
			body := strings.TrimPrefix(trimmed, ">")
			sb.WriteString("<blockquote>" + inlineMD(strings.TrimPrefix(body, " ")) + "</blockquote>\n")

		case reUL.MatchString(line):
			flushPara()
			openList("ul")
			sb.WriteString("<li>" + inlineMD(reUL.FindStringSubmatch(line)[1]) + "</li>\n")

		case reOL.MatchString(line):
			flushPara()
			openList("ol")
			sb.WriteString("<li>" + inlineMD(reOL.FindStringSubmatch(line)[1]) + "</li>\n")

		default:
			closeList()
			para = append(para, trimmed)
		}
	}
	if inFence && fence.Len() > 0 { // 未闭合围栏照样出，别吞内容
		sb.WriteString("<pre><code>" + html.EscapeString(fence.String()) + "</code></pre>\n")
	}
	flushPara()
	closeList()
	return sb.String()
}

// inlineMD 先整体转义，再按 Markdown 语法加标签——顺序反了就是 XSS。
func inlineMD(s string) string {
	s = html.EscapeString(s)

	// 行内 code 先抽走，否则里面的 * _ [ ] 会被后面的正则吃掉
	var codes []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, m[1:len(m)-1])
		return "\x00C" + itoa(len(codes)-1) + "\x00"
	})

	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		g := reBold.FindStringSubmatch(m)
		if g[1] != "" {
			return "<strong>" + g[1] + "</strong>"
		}
		return "<strong>" + g[2] + "</strong>"
	})
	s = reItalc.ReplaceAllStringFunc(s, func(m string) string {
		g := reItalc.FindStringSubmatch(m)
		if g[1] != "" {
			return "<em>" + g[1] + "</em>"
		}
		return "<em>" + g[2] + "</em>"
	})
	s = reImg.ReplaceAllString(s, `<img src="$2" alt="$1">`)
	s = reLink.ReplaceAllString(s, `<a href="$2">$1</a>`)

	s = strings.ReplaceAll(s, "\n", "<br>\n")

	for i, c := range codes {
		s = strings.Replace(s, "\x00C"+itoa(i)+"\x00", "<code>"+c+"</code>", 1)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
