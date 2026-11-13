package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 外壳页的 JS 一旦有语法错，整个页面的交互（菜单 / 拖放 / pvReplace）会**一起静默失效**，
// 而窗口照样画出来、标题照样对 —— 肉眼几乎看不出来。2026-11-13 就因为漏了一个 `}`，
// 拖拽调试了半小时。这道检查专门防这一下。
func TestShellJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过外壳 JS 语法检查（改 JS 后请手工 node --check）")
	}

	js := shellJS(EmptyPlan())
	if strings.TrimSpace(js) == "" {
		t.Fatal("shellJS 返回空")
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "shell.js")
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", p).CombinedOutput()
	if err != nil {
		t.Fatalf("外壳 JS 语法错：\n%s", out)
	}
}

// 外壳页必须自带 pgReplace 定义，否则 Go 侧的 Eval 是空转。
func TestShellJSHasReplaceHook(t *testing.T) {
	js := shellJS(EmptyPlan())
	for _, want := range []string{"window.pvReplace", "function setupMedia", "pvSend('dropblob:'", "pvSend('quit')"} {
		if !strings.Contains(js, want) {
			t.Errorf("外层 JS 缺 %q", want)
		}
	}
}

func TestBuildShellEmptyHasDropZone(t *testing.T) {
	h := buildShell(EmptyPlan())
	for _, want := range []string{"k-empty", "把文件拖进来", "拖入文件或 Ctrl+O", "文件(F)", "帮助(H)"} {
		if !strings.Contains(h, want) {
			t.Errorf("裸开外壳缺 %q", want)
		}
	}
}

// 画面拖拽拿路径那条通路（v0.3.0）在页面侧的三件套：
// 能力开关、把 File 回传给宿主、以及宿主没接住时的兜底渲染。
func TestShellJSHasHostDropPath(t *testing.T) {
	js := shellJS(EmptyPlan())
	for _, want := range []string{
		"postMessageWithAdditionalObjects",
		"__pvCanDropPath",
		"__pvHostDropPending",
		"sendDroppedFile(files[0])",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("画面拖拽通路缺 %q", want)
		}
	}
}

// 就地渲染时内容必须用 blob:/file: 那个来源，而不是硬拼路径。
func TestContentUsesBlobURL(t *testing.T) {
	p := &Plan{Name: "x.png", Kind: KindImage, BlobURL: "blob:file:///abc", Path: `C:\nope\x.png`}
	h := contentFor(p)
	if !strings.Contains(h, "blob:file:///abc") {
		t.Fatalf("没有用 BlobURL: %s", h)
	}
	if strings.Contains(h, "nope") {
		t.Fatalf("不该再落回 Path: %s", h)
	}
}
