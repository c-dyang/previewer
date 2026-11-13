package main

import (
	"strings"
	"testing"
)

func TestDecodeBytesUTF8(t *testing.T) {
	if got := decodeBytes([]byte("中文 abc")); got != "中文 abc" {
		t.Fatalf("UTF-8 = %q", got)
	}
}

func TestDecodeBytesBOM(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte("中文")...)
	if got := decodeBytes(withBOM); got != "中文" {
		t.Fatalf("UTF-8 BOM = %q", got)
	}
	// UTF-16LE "中文"
	le := []byte{0xFF, 0xFE, 0x2D, 0x4E, 0x87, 0x65}
	if got := decodeBytes(le); got != "中文" {
		t.Fatalf("UTF-16LE = %q", got)
	}
	// UTF-16BE "中文"
	be := []byte{0xFE, 0xFF, 0x4E, 0x2D, 0x65, 0x87}
	if got := decodeBytes(be); got != "中文" {
		t.Fatalf("UTF-16BE = %q", got)
	}
}

func TestDecodeBytesGBK(t *testing.T) {
	// "设备离线" 的 GBK
	gbk := []byte{0xC9, 0xE8, 0xB1, 0xB8, 0xC0, 0xEB, 0xCF, 0xDF}
	if got := decodeBytes(gbk); got != "设备离线" {
		t.Fatalf("GBK = %q, 期望 设备离线", got)
	}
}

func TestLooksText(t *testing.T) {
	if !looksText([]byte("abc\n中文\n")) {
		t.Error("普通文本应判定为文本")
	}
	if looksText([]byte{0x4d, 0x5a, 0x00, 0x00}) {
		t.Error("含 NUL 应判定为二进制")
	}
	if !looksText(nil) {
		t.Error("空内容按文本处理")
	}
	if !looksText([]byte{0xFF, 0xFE, 0x41, 0x00}) {
		t.Error("UTF-16 BOM 应判定为文本（即便含 NUL）")
	}
}

func TestHexDump(t *testing.T) {
	out := hexDump([]byte("hello"), 0)
	if !strings.HasPrefix(out, "00000000  68 65 6c 6c 6f") {
		t.Fatalf("hex 行首不对: %q", out)
	}
	if !strings.Contains(out, "|hello|") {
		t.Fatalf("ASCII 侧栏缺失: %q", out)
	}
	// 多行：偏移按 16 递增
	out2 := hexDump(make([]byte, 40), 0x100)
	lines := strings.Split(strings.TrimRight(out2, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("40 字节应为 3 行, 实得 %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "00000100") || !strings.HasPrefix(lines[2], "00000120") {
		t.Fatalf("偏移不对: %q / %q", lines[0], lines[2])
	}
}

func TestSniffCodec(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "x.mp4", []byte("\x00\x00\x00\x18ftypisom....avc1...."))
	if got := sniffMP4Codec(p, 24); got != "avc1" {
		t.Fatalf("sniffMP4Codec = %q, 期望 avc1", got)
	}
	p2 := writeFile(t, dir, "y.mkv", []byte("\x1aE\xdf\xa3....V_MPEGH/ISO/HEVC...."))
	if got := sniffMKVCodec(p2, 32); got != "hvc1" {
		t.Fatalf("sniffMKVCodec = %q, 期望 hvc1", got)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1 KB",
		1536:    "1.5 KB",
		1048576: "1 MB",
		1000973: "977.5 KB",
	}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, 期望 %q", n, got, want)
		}
	}
}

func TestMenusAccelUnique(t *testing.T) {
	plan := &Plan{Kind: KindText}
	seen := map[string]bool{}
	for _, m := range menusFor(plan) {
		if seen[m.Accel] {
			t.Errorf("菜单快捷键 %q 重复", m.Accel)
		}
		seen[m.Accel] = true
		if m.Name == "" || m.Accel == "" {
			t.Errorf("菜单缺名或缺加速键: %+v", m)
		}
	}
	if len(seen) != 4 {
		t.Errorf("应有 4 个菜单, 实得 %d", len(seen))
	}
}

// 无媒体时控制菜单必须整组置灰，而不是消失 —— 菜单条宽度要恒定。
func TestControlMenuDisabledWithoutMedia(t *testing.T) {
	for _, m := range menusFor(&Plan{Kind: KindText}) {
		if m.Name != "控制" {
			continue
		}
		for _, it := range m.Items {
			if it.Sep {
				continue
			}
			if !it.Dis {
				t.Errorf("无媒体时 %q 应为置灰", it.Label)
			}
		}
	}
	for _, m := range menusFor(&Plan{Kind: KindVideo}) {
		if m.Name != "控制" {
			continue
		}
		for _, it := range m.Items {
			if !it.Sep && it.Dis {
				t.Errorf("有媒体时 %q 不该置灰", it.Label)
			}
		}
	}
}

func TestFileURL(t *testing.T) {
	if got := fileURL(`C:\a\b.png`); got != "file:///C:/a/b.png" {
		t.Fatalf("fileURL = %q", got)
	}
	got := fileURL(`C:\中文\b.pdf`)
	if !strings.HasPrefix(got, "file:///C:/") || !strings.Contains(got, "%E4%B8%AD") {
		t.Fatalf("中文路径未编码: %q", got)
	}
}
