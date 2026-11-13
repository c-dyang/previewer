package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写夹具失败 %s: %v", p, err)
	}
	return p
}

// fakePlayers 造一个"本机有播放器"的后端表；空表表示一个都没有。
func fakePlayers() *Backends {
	return &Backends{Players: []Player{
		{Name: "mpv", Path: `C:\fake\mpv.exe`, Video: true, Audio: true, Image: true},
	}}
}

func TestResolveRoutes(t *testing.T) {
	dir := t.TempDir()

	h264 := append([]byte("\x00\x00\x00\x18ftypmp42"), []byte("....avc1....moov")...)
	hevc := append([]byte("\x00\x00\x00\x18ftypmp42"), []byte("....hvc1....moov")...)
	noCodec := []byte("\x00\x00\x00\x18ftypmp42....mdat....")
	mkvBytes := []byte("\x1aE\xdf\xa3....V_MPEGH/ISO/HEVC....")

	gbk := []byte{0xC9, 0xE8, 0xB1, 0xB8, 0xC0, 0xEB, 0xCF, 0xDF} // "设备离线" 的 GBK 编码

	cases := []struct {
		name     string
		file     string
		data     []byte
		be       *Backends
		wantKind Kind
		wantRend Renderer
		wantDeg  string
	}{
		{"markdown", "a.md", []byte("# 标题\n\n正文"), &Backends{}, KindMarkdown, RShell, DegNone},
		{"纯文本", "a.txt", []byte("hello\nworld\n"), &Backends{}, KindText, RShell, DegNone},
		{"GBK 文本", "gbk.log", gbk, &Backends{}, KindText, RShell, DegNone},
		{"空文件", "empty.txt", nil, &Backends{}, KindText, RShell, DegEmptyFile},
		{"未知二进制", "blob.dat", []byte{0, 1, 2, 3, 0, 9, 8}, &Backends{}, KindBinary, RShell, DegBinaryUnknown},
		{"图片 png", "a.png", []byte("\x89PNG\r\n\x1a\n"), &Backends{}, KindImage, RShell, DegNone},
		{"PDF", "a.pdf", []byte("%PDF-1.4\n"), &Backends{}, KindPDF, RShell, DegNone},
		{"HTML 直通", "a.html", []byte("<h1>x</h1>"), &Backends{}, KindHTML, RDirect, DegNone},

		{"mp4/h264 原生", "avc.mp4", h264, &Backends{}, KindVideo, RMedia, DegNone},
		{"mp4/hevc 降级", "hevc.mp4", hevc, fakePlayers(), KindVideo, RExternal, DegCodec},
		{"mp4/hevc 无后端", "hevc2.mp4", hevc, &Backends{}, KindVideo, RExternal, DegNoExternal},
		{"mp4 编码未识别", "unk.mp4", noCodec, &Backends{}, KindVideo, RMedia, DegCodecUnknown},
		{"mkv 换引擎", "a.mkv", mkvBytes, fakePlayers(), KindVideo, RExternal, DegContainer},
		{"mkv 无后端", "b.mkv", mkvBytes, &Backends{}, KindVideo, RExternal, DegNoExternal},

		{"tif 换解码器", "a.tif", []byte("II*\x00"), fakePlayers(), KindImage, RExternal, DegImageFormat},
		{"tif 无后端", "b.tif", []byte("II*\x00"), &Backends{}, KindImage, RExternal, DegNoExternal},

		{"mp3 原生", "a.mp3", []byte("ID3"), &Backends{}, KindAudio, RMedia, DegNone},
		{"ape 降级", "a.ape", []byte("MAC "), fakePlayers(), KindAudio, RExternal, DegContainer},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeFile(t, dir, c.file, c.data)
			plan, err := Resolve(p, c.be)
			if err != nil {
				t.Fatalf("Resolve 失败: %v", err)
			}
			if plan.Kind != c.wantKind {
				t.Errorf("Kind = %s, 期望 %s", plan.Kind, c.wantKind)
			}
			if plan.Renderer != c.wantRend {
				t.Errorf("Renderer = %s, 期望 %s", plan.Renderer, c.wantRend)
			}
			if plan.Degraded != c.wantDeg {
				t.Errorf("Degraded = %q, 期望 %q", plan.Degraded, c.wantDeg)
			}
			// 外部路由必须真的带出一个后端路径，否则是"假装降级成功"
			if plan.Renderer == RExternal && c.wantDeg != DegNoExternal && plan.Backend == "" {
				t.Errorf("Renderer=external 但没有 Backend")
			}
		})
	}
}

func TestResolveGBKIsDecoded(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "gbk.txt", []byte{0xC9, 0xE8, 0xB1, 0xB8, 0xC0, 0xEB, 0xCF, 0xDF})
	plan, err := Resolve(p, &Backends{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Text != "设备离线" {
		t.Fatalf("GBK 解码结果 = %q, 期望 设备离线", plan.Text)
	}
}

func TestResolveDirectoryRejected(t *testing.T) {
	if _, err := Resolve(t.TempDir(), &Backends{}); err == nil {
		t.Fatal("目录应当被拒绝")
	}
}

func TestResolveMissingFile(t *testing.T) {
	if _, err := Resolve(filepath.Join(t.TempDir(), "nope.txt"), &Backends{}); err == nil {
		t.Fatal("不存在的文件应当报错")
	}
}

func TestTextTruncation(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("0123456789abcdef", (textCap/16)+64)
	p := writeFile(t, dir, "big.txt", []byte(big))
	plan, err := Resolve(p, &Backends{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Truncated {
		t.Fatal("超大文本应标记截断")
	}
	if plan.Degraded != DegTextTruncated {
		t.Fatalf("Degraded = %q, 期望 %q", plan.Degraded, DegTextTruncated)
	}
	if len(plan.Text) > textCap {
		t.Fatalf("截断后长度 %d 超过上限 %d", len(plan.Text), textCap)
	}
}

func TestDegradedLabelCoversAll(t *testing.T) {
	all := []string{DegNone, DegContainer, DegCodec, DegCodecUnknown, DegImageFormat,
		DegTextTruncated, DegNoExternal, DegEmptyFile, DegBinaryUnknown}
	for _, d := range all {
		if lbl := DegradedLabel(&Plan{Degraded: d}); d != DegNone && lbl == "" {
			t.Errorf("原因码 %q 没有中文解释", d)
		}
	}
	// 未知原因码要原样透出，不能变成空串
	if got := DegradedLabel(&Plan{Degraded: "something_new"}); got != "something_new" {
		t.Errorf("未知原因码 = %q, 期望原样返回", got)
	}
}
