package modelmanager

// 本文件包含需要真实网络的冒烟测试，默认跳过。
//
// 运行方式（任选其一）：
//
//	$env:MODELMANAGER_NET_TEST="1"; go test ./internal/modelmanager/ -run TestNetworkSmoke -v
//	go test ./internal/modelmanager/ -run TestNetworkSmoke -v -args -net=1
//
// 该测试会真实下载 tiny 模型的全部文件（约 76MB），用于验证默认镜像
// hf-mirror 在本机可达、URL 模板与清单中的文件大小与真实仓库一致。
// 没有设置环境变量时完全跳过，保证 CI 与离线环境可跑。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// netTestEnabled 判断是否启用联网测试。
func netTestEnabled() bool {
	return os.Getenv("MODELMANAGER_NET_TEST") == "1"
}

func TestNetworkSmokeDownloadTinyFromDefaultMirror(t *testing.T) {
	if !netTestEnabled() {
		t.Skip("跳过联网冒烟测试：设置 MODELMANAGER_NET_TEST=1 后启用")
	}

	root := t.TempDir()
	m := New(Options{
		RootDir:    root,
		MaxRetries: 2,
	})

	// 校验默认镜像顺序。
	ms := m.Mirrors()
	if len(ms) == 0 || ms[0].ID != "hf-mirror" {
		t.Fatalf("默认首选镜像应为 hf-mirror，实际：%+v", ms)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var lastPct int = -1
	dir, err := m.Ensure(ctx, "tiny", func(ratio float64, msg string) {
		pct := int(ratio * 100)
		if pct != lastPct {
			lastPct = pct
			t.Logf("进度 %3d%% %s", pct, msg)
		}
	})
	if err != nil {
		t.Fatalf("真实下载失败：%v", err)
	}

	entry, ok := m.Find("tiny")
	if !ok {
		t.Fatal("找不到 tiny")
	}
	for _, f := range entry.Files {
		info, statErr := os.Stat(filepath.Join(dir, f.Path))
		if statErr != nil {
			t.Fatalf("文件 %s 缺失：%v", f.Path, statErr)
		}
		if info.Size() != f.Size {
			t.Fatalf("文件 %s 实际大小 %d 与清单 %d 不一致（清单需要更新）", f.Path, info.Size(), f.Size)
		}
	}

	installed, err := m.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if installed["tiny"] != dir {
		t.Fatalf("未标记为已安装：%v", installed)
	}
	t.Logf("下载完成：%s", dir)
}
