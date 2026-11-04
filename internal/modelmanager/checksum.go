package modelmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// SHA256File 计算文件内容的 SHA-256 摘要（小写十六进制）。
//
// 用于可选的完整性校验：清单中 ManifestFile.SHA256 非空时，下载完成后会调用它
// 比对摘要；清单留空则跳过校验。HF 上 LFS 文件的 sha256 需要调用 Hub API
// （/api/models/<repo>）才能获得，本包刻意不为此发起额外网络请求，
// 保持模型管理在纯镜像环境下也能离线工作。
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败：%w", err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 256*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", fmt.Errorf("读取文件失败：%w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
