package modelmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// Repo 下载来源的解析。
//
// 支持两种用户输入：
//   - 仓库名：`Systran/faster-whisper-small`、`viethang/cohere-transcribe-03-2026`
//   - 完整链接：`https://hf-mirror.com/Systran/faster-whisper-small`、
//     `https://huggingface.co/CohereLabs/cohere-transcribe-03-2026/tree/main`
//
// 无论哪种形式，最终都归一化成 `org/name` 交给镜像拼接
// `{base}/{repo}/resolve/main/{file}`，这样"国内源"与"官方源"可以共用一套逻辑。

// ParseRepoInput 从用户输入里解析出 `org/name` 形式的仓库名。
//
// 返回中文错误，便于直接展示给用户。
func ParseRepoInput(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("请填写模型仓库名或 Hugging Face 链接")
	}
	// 去掉 URL 的协议与主机部分，只保留路径。
	if strings.Contains(text, "://") {
		rest := text
		if idx := strings.Index(rest, "://"); idx >= 0 {
			rest = rest[idx+3:]
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", fmt.Errorf("链接里没有仓库路径：%s", text)
		}
		text = rest[slash+1:]
	}
	text = strings.Trim(text, "/")
	// 去掉 HF 页面链接常见的尾部路径。
	for _, suffix := range []string{"/tree/main", "/tree/master", "/blob/main", "/resolve/main"} {
		if strings.HasSuffix(text, suffix) {
			text = strings.TrimSuffix(text, suffix)
		}
	}
	text = strings.Trim(text, "/")
	parts := strings.Split(text, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("仓库名应形如 org/name，实际是：%s", raw)
	}
	org := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if org == "" || name == "" {
		return "", fmt.Errorf("仓库名不完整：%s", raw)
	}
	if strings.ContainsAny(org+name, " \t") {
		return "", fmt.Errorf("仓库名不能包含空格：%s", raw)
	}
	return org + "/" + name, nil
}

// RepoFiles 列出远端仓库里的文件及其大小。
//
// 走 Hugging Face 的 `api/models/{repo}/tree/main` 接口（镜像站同样支持），
// 只读元数据，不会下载权重。用户用链接添加模型时靠它自动生成文件清单，
// 免得手工填十几个文件名与字节数。
func (m *ManagerImpl) RepoFiles(ctx context.Context, repo string) ([]ManifestFile, error) {
	normalized, err := ParseRepoInput(repo)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, mi := range m.usableMirrors() {
		files, err := m.repoFilesFrom(ctx, mi, normalized)
		if err == nil {
			return files, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的镜像")
	}
	return nil, fmt.Errorf("读取仓库 %s 的文件列表失败：%w", normalized, lastErr)
}

// treeEntry 是 tree 接口的元素。
type treeEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// repoFilesFrom 从单个镜像读取文件列表。
func (m *ManagerImpl) repoFilesFrom(ctx context.Context, mi Mirror, repo string) ([]ManifestFile, error) {
	endpoint := strings.TrimRight(mi.BaseURL, "/") + "/api/models/" + escapeRepo(repo) + "/tree/main"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	m.decorate(req)
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		m.markMirrorDown(mi.ID)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errors.New("仓库是受限的，需要在设置里配置 Hugging Face 令牌")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("接口返回 %d", resp.StatusCode)
	}

	var entries []treeEntry
	// tree 接口的响应是数组，最多几 MB；限流读取避免异常响应打爆内存。
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, readErr
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("无法解析仓库文件列表：%v", err)
	}

	files := make([]ManifestFile, 0, len(entries))
	for _, e := range entries {
		if e.Type != "" && e.Type != "file" {
			continue
		}
		if e.Size <= 0 {
			continue
		}
		// 跳过与推理无关的目录与隐藏文件。
		if strings.HasPrefix(e.Path, ".") || strings.Contains(e.Path, "/.") {
			continue
		}
		if isNonEssentialPath(e.Path) {
			continue
		}
		files = append(files, ManifestFile{Path: e.Path, Size: e.Size})
	}
	if len(files) == 0 {
		return nil, errors.New("仓库里没有可下载的文件")
	}
	return files, nil
}

// isNonEssentialPath 判断是否为推理不需要的文件。
func isNonEssentialPath(path string) bool {
	for _, prefix := range []string{"assets/", "demo/", "images/", "samples/", ".eval_results/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "readme.md", ".gitattributes":
		return true
	}
	return false
}

// AddRepoModel 用用户提供的仓库地址新增一个可下载模型。
//
// 用于「不在内置清单里的模型」：自动读取仓库文件清单，按体积生成条目。
// 引擎按 InferenceKind 区分，默认按仓库里是否有 model.bin 自动判断。
func (m *ManagerImpl) AddRepoModel(ctx context.Context, id, rawRepo, engine string) (CatalogEntry, error) {
	repo, err := ParseRepoInput(rawRepo)
	if err != nil {
		return CatalogEntry{}, err
	}
	name := strings.TrimSpace(id)
	if name == "" {
		name = repo[strings.LastIndex(repo, "/")+1:]
	}
	name = sanitizeDirName(name)

	m.mu.RLock()
	for _, e := range m.catalog {
		if strings.EqualFold(e.ID, name) {
			m.mu.RUnlock()
			return CatalogEntry{}, fmt.Errorf("模型标识 %q 已被占用，请换一个名称", name)
		}
	}
	m.mu.RUnlock()

	files, err := m.RepoFiles(ctx, repo)
	if err != nil {
		return CatalogEntry{}, err
	}

	// 自动判断引擎：有 model.bin 说明是 CTranslate2，否则按 transformers 处理。
	detected := strings.ToLower(strings.TrimSpace(engine))
	if detected != EngineFasterWhisper && detected != EngineTransformers {
		detected = EngineTransformers
		for _, f := range files {
			if f.Path == ct2ModelFileName {
				detected = EngineFasterWhisper
				break
			}
		}
	}

	required := make([]string, 0, len(files))
	total := int64(0)
	for _, f := range files {
		required = append(required, f.Path)
		total += f.Size
	}
	// transformers 仓储常带 .safetensors 之外的训练残留，按必需文件全集校验
	// 太苛刻；这里只把关键文件列为必需。
	required = essentialFiles(files, detected)

	entry := CatalogEntry{
		ID:          name,
		Name:        "自定义模型 · " + repo,
		Repo:        repo,
		Description: "用户提供的下载地址（" + detected + " 格式），文件清单读自仓库。",
		SizeBytes:   total,
		Files:       files,
		Required:    required,
		Engine:      detected,
	}

	m.mu.Lock()
	m.catalog = append(m.catalog, entry)
	m.mu.Unlock()
	return entry, nil
}

// essentialFiles 从文件清单里挑出「缺少就无法加载」的关键文件。
func essentialFiles(files []ManifestFile, engine string) []string {
	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f.Path] = true
	}
	pick := func(candidates ...string) string {
		for _, c := range candidates {
			if has[c] {
				return c
			}
		}
		return ""
	}
	required := make([]string, 0, 4)
	add := func(path string) {
		if path != "" {
			required = append(required, path)
		}
	}
	if engine == EngineFasterWhisper {
		add(pick("config.json"))
		add(pick("model.bin"))
		add(pick("tokenizer.json"))
		add(pick("vocabulary.txt", "vocabulary.json"))
		return required
	}
	add(pick("config.json"))
	add(pick("model.safetensors", "pytorch_model.bin"))
	add(pick("tokenizer.json", "tokenizer.model"))
	if len(required) == 0 {
		// 认不出结构时退化为「全部文件必需」，至少不会把半成品当已安装。
		for _, f := range files {
			required = append(required, f.Path)
		}
	}
	return required
}
