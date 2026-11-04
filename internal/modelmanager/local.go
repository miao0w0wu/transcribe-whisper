package modelmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 本地模型目录的识别依据。
//
// 用户自选的目录可能是任意下载工具留下的：
//   - faster-whisper / CTranslate2 目录：有 model.bin
//   - transformers 目录：有 model.safetensors（或 pytorch_model.bin）
//
// 这里按「存在哪些标志文件」判断格式，不去解析配置内容，
// 避免因为版本差异读不懂 config.json 就误判。
const (
	ct2ModelFileName   = "model.bin"
	ct2ConfigFileName  = "config.json"
	safeTensorsName    = "model.safetensors"
	pytorchModelName   = "pytorch_model.bin"
	tokenizerJSONName  = "tokenizer.json"
	vocabTxtName       = "vocabulary.txt"
	vocabJSONName      = "vocabulary.json"
	minModelFileBytes  = 1024 // 小于 1KB 的权重文件基本可以断定是占位或损坏
	localProbeMaxDepth = 3
)

// DetectLocalModel 检查一个本地目录能否作为识别模型使用。
//
// 返回识别到的格式（EngineFasterWhisper / EngineTransformers）与缺失的关键
// 文件说明。不返回错误表示目录可用；返回错误时错误信息是给用户看的中文原因。
//
// 检测是「严格但友好」的：缺少 tokenizer 或词表时仍然判定不可用，因为
// faster-whisper 与 transformers 都会在加载时报 “Cannot load the vocabulary”
// 之类的底层错误，与其让用户看不懂，不如在选目录时就讲清楚缺什么。
func DetectLocalModel(dir string) (engine string, files []string, err error) {
	path := strings.TrimSpace(dir)
	if path == "" {
		return "", nil, errors.New("请先选择模型目录")
	}
	abs, absErr := filepath.Abs(path)
	if absErr == nil {
		path = abs
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", nil, fmt.Errorf("目录不存在：%s", path)
		}
		return "", nil, fmt.Errorf("无法访问目录 %s：%v", path, statErr)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("这不是目录：%s", path)
	}

	// 有些用户的目录多套了一层，例如 <模型根>/small/model.bin。
	// 向下探一层，避免非要用户手动点进子目录。
	root, ok := findModelRoot(path)
	if !ok {
		return "", nil, fmt.Errorf(
			"目录里没有找到模型权重文件，请选择包含 %s 或 %s 的目录",
			ct2ModelFileName, safeTensorsName)
	}

	ct2 := fileExists(filepath.Join(root, ct2ModelFileName))
	transformers := fileExists(filepath.Join(root, safeTensorsName)) ||
		fileExists(filepath.Join(root, pytorchModelName))

	switch {
	case ct2:
		if !fileExists(filepath.Join(root, ct2ConfigFileName)) {
			return "", nil, fmt.Errorf("CTranslate2 模型缺少 %s：%s", ct2ConfigFileName, root)
		}
		// 词表文件二选一：large-v3 用 vocabulary.json，其余用 vocabulary.txt。
		// 两个都没有时 faster-whisper 会在加载阶段报错，这里提前拦下。
		if !fileExists(filepath.Join(root, vocabTxtName)) && !fileExists(filepath.Join(root, vocabJSONName)) {
			return "", nil, fmt.Errorf(
				"模型缺少词表文件（%s 或 %s），faster-whisper 无法加载：%s",
				vocabTxtName, vocabJSONName, root)
		}
		if !fileExists(filepath.Join(root, tokenizerJSONName)) {
			return "", nil, fmt.Errorf("模型缺少 %s，faster-whisper 无法加载：%s", tokenizerJSONName, root)
		}
		return EngineFasterWhisper, []string{ct2ConfigFileName, ct2ModelFileName, tokenizerJSONName}, nil
	case transformers:
		if !fileExists(filepath.Join(root, ct2ConfigFileName)) {
			return "", nil, fmt.Errorf("transformers 模型缺少 %s：%s", ct2ConfigFileName, root)
		}
		return EngineTransformers, []string{ct2ConfigFileName, safeTensorsName}, nil
	default:
		return "", nil, fmt.Errorf("无法识别的模型格式：%s", root)
	}
}

// findModelRoot 在给定目录及其子目录里寻找模型根目录。
//
// 优先返回当前目录本身；否则向下最多 localProbeMaxDepth 层找第一个含权重
// 文件的目录。这样用户选中「上一层目录」也能正确识别。
func findModelRoot(dir string) (string, bool) {
	if hasModelWeights(dir) {
		return dir, true
	}
	found := ""
	var walk func(path string, depth int) bool
	walk = func(path string, depth int) bool {
		if depth > localProbeMaxDepth {
			return false
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return false
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // 稳定顺序，避免每次选到不同的子目录
		for _, name := range names {
			child := filepath.Join(path, name)
			// 跳过隐藏目录与明显的缓存/临时目录，减少误判。
			if strings.HasPrefix(name, ".") || strings.Contains(name, ".deleting-") {
				continue
			}
			if hasModelWeights(child) {
				found = child
				return true
			}
		}
		for _, name := range names {
			child := filepath.Join(path, name)
			if strings.HasPrefix(name, ".") || strings.Contains(name, ".deleting-") {
				continue
			}
			if walk(child, depth+1) {
				return true
			}
		}
		return false
	}
	if walk(dir, 0) {
		return found, true
	}
	return "", false
}

// hasModelWeights 判断目录里是否有像样的权重文件。
func hasModelWeights(dir string) bool {
	for _, name := range []string{ct2ModelFileName, safeTensorsName, pytorchModelName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !info.IsDir() && info.Size() >= minModelFileBytes {
			return true
		}
	}
	return false
}

// fileExists 判断路径是否为存在的非空文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// AddLocalModel 把一个本地目录注册为可直接使用的识别模型。
//
// 本地模型不进入 rootDir，也不参与下载与删除：它只是被记进内存清单，
// 让 Ensure 直接返回用户给的目录。id 冲突时返回错误，避免覆盖内置模型
// 或另一个已经注册过的本地目录。
func (m *ManagerImpl) AddLocalModel(id, dir string) (CatalogEntry, error) {
	engine, required, err := DetectLocalModel(dir)
	if err != nil {
		return CatalogEntry{}, err
	}
	abs, absErr := filepath.Abs(strings.TrimSpace(dir))
	if absErr != nil {
		abs = strings.TrimSpace(dir)
	}
	// 目录被向下探过一层时，用真正含权重的目录，避免后面加载失败。
	if root, ok := findModelRoot(abs); ok {
		abs = root
	}

	name := strings.TrimSpace(id)
	if name == "" {
		name = filepath.Base(abs)
	}
	name = sanitizeDirName(name)

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.catalog {
		if strings.EqualFold(e.ID, name) {
			return CatalogEntry{}, fmt.Errorf("模型标识 %q 已被占用，请换一个名称", name)
		}
	}

	entry := CatalogEntry{
		ID:          name,
		Name:        "本地模型 · " + filepath.Base(abs),
		Repo:        "",
		Description: "用户自选的本地模型目录（" + engine + " 格式）。不会下载，也不会被删除。",
		Files:       nil,
		Required:    required,
		Engine:      engine,
		Custom:      true,
	}
	m.catalog = append(m.catalog, entry)
	// 记录本地目录，Ensure / LocalDir 优先返回它。
	if m.localDirs == nil {
		m.localDirs = make(map[string]string)
	}
	m.localDirs[name] = abs
	return entry, nil
}

// RemoveLocalModel 从清单与本地目录记录里移除一个自选模型。
//
// 只解除注册，绝不删除磁盘上的文件：那是用户自己的模型目录。
func (m *ManagerImpl) RemoveLocalModel(id string) {
	name := strings.TrimSpace(id)
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := make([]CatalogEntry, 0, len(m.catalog))
	for _, e := range m.catalog {
		if e.Custom && (strings.EqualFold(e.ID, name) || strings.EqualFold(m.localDirs[e.ID], name)) {
			delete(m.localDirs, e.ID)
			continue
		}
		filtered = append(filtered, e)
	}
	m.catalog = filtered
}

// LocalDirs 返回已注册的本地模型目录（只读副本）。
func (m *ManagerImpl) LocalDirs() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.localDirs))
	for k, v := range m.localDirs {
		out[k] = v
	}
	return out
}

// localDirOf 返回已注册的本地目录，未注册时返回空串。
func (m *ManagerImpl) localDirOf(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if dir, ok := m.localDirs[id]; ok {
		return dir
	}
	// 允许用别名查询（大小写、Repo 形式）。
	for key, dir := range m.localDirs {
		if strings.EqualFold(key, id) {
			return dir
		}
	}
	return ""
}

// validateLocalRegistered 在 Ensure 前确认本地模型目录仍然存在。
func (m *ManagerImpl) validateLocalRegistered(entry CatalogEntry) error {
	if !entry.Custom {
		return nil
	}
	dir := m.localDirOf(entry.ID)
	if dir == "" {
		return fmt.Errorf("本地模型 %q 未注册目录", entry.ID)
	}
	if _, _, err := DetectLocalModel(dir); err != nil {
		return fmt.Errorf("本地模型目录不可用：%w", err)
	}
	return nil
}

// ctxErrOr 在 ctx 已取消时返回 ctx 的错误，否则返回 fallback。
func ctxErrOr(ctx context.Context, fallback error) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fallback
}
