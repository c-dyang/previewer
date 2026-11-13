package main

import (
	"strings"
	"testing"
)

func TestMarkdownHeadings(t *testing.T) {
	out := renderMarkdown("# 一级\n\n## 二级\n")
	if !strings.Contains(out, "<h1>一级</h1>") || !strings.Contains(out, "<h2>二级</h2>") {
		t.Fatalf("标题未渲染: %q", out)
	}
}

func TestMarkdownFrontmatterStripped(t *testing.T) {
	src := "---\nname: demo\nversion: \"1.0\"\n---\n# 正文\n"
	out := renderMarkdown(src)
	if strings.Contains(out, "name: demo") {
		t.Fatalf("frontmatter 没被丢掉: %q", out)
	}
	if !strings.Contains(out, "<h1>正文</h1>") {
		t.Fatalf("正文丢了: %q", out)
	}
}

func TestMarkdownFenceNotParsed(t *testing.T) {
	src := "```go\n# 这不是标题\n**也不是粗体**\n```\n"
	out := renderMarkdown(src)
	if strings.Contains(out, "<h1>") || strings.Contains(out, "<strong>") {
		t.Fatalf("围栏内被误解析: %q", out)
	}
	if !strings.Contains(out, "<pre><code") || !strings.Contains(out, "class=\"lang-go\"") {
		t.Fatalf("围栏代码块结构不对: %q", out)
	}
}

func TestMarkdownListsAndQuote(t *testing.T) {
	out := renderMarkdown("- a\n- b\n\n1. x\n2. y\n\n> 引用\n")
	if !strings.Contains(out, "<ul>\n<li>a</li>") {
		t.Fatalf("无序列表不对: %q", out)
	}
	if !strings.Contains(out, "<ol>\n<li>x</li>") {
		t.Fatalf("有序列表不对: %q", out)
	}
	if !strings.Contains(out, "<blockquote>引用</blockquote>") {
		t.Fatalf("引用不对: %q", out)
	}
}

func TestMarkdownInline(t *testing.T) {
	out := renderMarkdown("**粗** 和 *斜* 和 `code` 和 [链](http://a.b) 和 ![图](c.png)")
	for _, want := range []string{
		"<strong>粗</strong>", "<em>斜</em>", "<code>code</code>",
		`<a href="http://a.b">链</a>`, `<img src="c.png" alt="图">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺 %q，实际 %q", want, out)
		}
	}
}

// 转义必须先于加标签，否则 Markdown 里的 HTML 会真的执行。
func TestMarkdownEscapesHTML(t *testing.T) {
	out := renderMarkdown("<script>alert(1)</script>\n")
	if strings.Contains(out, "<script>") {
		t.Fatalf("HTML 未被转义: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("转义形式不对: %q", out)
	}
	// 行内 code 里的标签也要转义
	out2 := renderMarkdown("用 `<img>` 标签")
	if strings.Contains(out2, "<code><img>") {
		t.Fatalf("行内 code 未转义: %q", out2)
	}
}

func TestMarkdownHR(t *testing.T) {
	out := renderMarkdown("a\n\n---\n\nb\n")
	if !strings.Contains(out, "<hr>") {
		t.Fatalf("分隔线未渲染: %q", out)
	}
	// *** 也会成为分隔线，不应崩
	if out := renderMarkdown("***\n"); !strings.Contains(out, "<hr>") {
		t.Fatalf("*** 未识别为分隔线: %q", out)
	}
}

func TestMarkdownUnclosedFence(t *testing.T) {
	out := renderMarkdown("```\nabc\n")
	if !strings.Contains(out, "abc") {
		t.Fatalf("未闭合围栏吞内容了: %q", out)
	}
}

func TestMarkdownEmpty(t *testing.T) {
	if out := renderMarkdown(""); out != "" {
		t.Fatalf("空输入应得空输出, 实得 %q", out)
	}
}
