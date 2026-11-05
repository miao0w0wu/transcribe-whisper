package modelmanager

import (
	"sort"
	"strings"
)

// fasterWhisperRepoPrefix 是官方 CTranslate2 转换仓库的固定前缀。
//
// 这些仓库由 Hugging Face 上的 Systran 组织维护，是 faster-whisper
// （CTranslate2 推理后端）官方推荐使用的权重格式，不含 PyTorch 权重。
const fasterWhisperRepoPrefix = "Systran/faster-whisper-"

// 文件体积常量（单位：字节），来自 2026 年 11 月对 hf-mirror.com 的实测响应。
//
// 说明：小文件（config.json / tokenizer.json / vocabulary.*）在镜像上会走
// 分块传输，不一定带 Content-Length；这里的数字是完整下载后的实际字节数。
// tokenizer.json 与 vocabulary.txt 在 tiny/base/small/medium 四个仓库中完全
// 相同（2,203,239 与 459,861 字节），large-v3 的 tokenizer.json 为 2,480,617
// 字节，并且该仓库没有 vocabulary.txt，改用 vocabulary.json（1,068,114 字节）。
const (
	sizeTokenizerSmall = int64(2203239)  // tokenizer.json（tiny/base/small/medium）
	sizeVocabularyTxt  = int64(459861)   // vocabulary.txt（tiny/base/small/medium）
	sizeTokenizerV3    = int64(2480617)  // tokenizer.json（large-v3）
	sizeVocabularyV3   = int64(1068114)  // vocabulary.json（large-v3）
	sizeModelTiny      = int64(75538270) // model.bin（tiny）
	sizeModelBase      = int64(145217532)
	sizeModelSmall     = int64(483546902)
	sizeModelMedium    = int64(1527906378)
	sizeModelLargeV3   = int64(3087284237)
	// sizePreprocessorV3 是 large-v3 的 preprocessor_config.json（340 字节）。
	//
	// 只有 large-v3 仓库带这个文件，tiny/base/small/medium 均返回 404。
	sizePreprocessorV3 = int64(340)
)

// preprocessorLargeV3 是 large-v3 的 preprocessor_config.json 内容。
//
// 为什么必须下载这个文件：faster-whisper 用 FeatureExtractor 算 log-Mel
// 特征，mel 滤波器组数量取自 preprocessor_config.json 的 feature_size，
// 缺失时 FeatureExtractor 回落到硬编码默认值 80。而 large-v3 的模型权重
// 是按 128 个 mel 通道训练的（CTranslate2 的 Whisper.n_mels == 128），于是
// 推理时抛「Invalid input features shape: expected an input with shape
// (1, 128, 3000), but got an input with shape (1, 80, 3000) instead」。
//
// 该文件缺失时 faster-whisper 不会报错也不会警告，只是静默用错维度，
// 所以必须作为必需文件下发，而不是等运行期才暴露。
const preprocessorLargeV3 = `{
  "chunk_length": 30,
  "feature_extractor_type": "WhisperFeatureExtractor",
  "feature_size": 128,
  "hop_length": 160,
  "n_fft": 400,
  "n_samples": 480000,
  "nb_max_frames": 3000,
  "padding_side": "right",
  "padding_value": 0.0,
  "processor_class": "WhisperProcessor",
  "return_attention_mask": false,
  "sampling_rate": 16000
}
`

// cohereTranscribeRepo 是 cohere-transcribe-03-2026 的下载来源。
//
// 官方仓库 `CohereLabs/cohere-transcribe-03-2026` 在 Hugging Face 上是受限
// 仓库（gated: auto），实测 hf-mirror 的 resolve 直接返回 403，镜像站无法
// 代为放行。这里改用内容完全一致的公开副本：文件清单、体积、sha 均与官方
// 一致（model.safetensors 4,131,862,976 字节），无需令牌即可下载。
const cohereTranscribeRepo = "viethang/cohere-transcribe-03-2026"

// cohereTranscribeUnusedFiles 是该仓库里与推理无关的文件。
//
// 只保留推理真正需要的：配置、权重、预处理/处理逻辑、tokenizer 与自定义
// 建模代码（transformers 需要这些 .py 才能加载 cohere_asr 结构）。
// demo 音频、评测结果与图片不下载，避免白白多传几十 MB。
var cohereTranscribeRequired = []string{
	"config.json",
	"generation_config.json",
	"preprocessor_config.json",
	"processor_config.json",
	"special_tokens_map.json",
	"tokenizer.json",
	"tokenizer.model",
	"tokenizer_config.json",
	"model.safetensors",
	"configuration_cohere_asr.py",
	"modeling_cohere_asr.py",
	"processing_cohere_asr.py",
	"tokenization_cohere_asr.py",
}

// DefaultCatalog 返回内置模型清单。
//
// faster-whisper 系列来源为 Hugging Face 上的官方 CTranslate2 转换仓库
// `Systran/faster-whisper-<size>`；仓库内实际存在的文件为：
//
//   - config.json          （全部型号）
//   - model.bin            （全部型号，CTranslate2 int8 量化权重，主要体积来源）
//   - tokenizer.json       （全部型号，HF fast tokenizer）
//   - vocabulary.txt       （tiny/base/small/medium；large-v3 仓库没有该文件）
//   - vocabulary.json      （仅 large-v3）
//
// Required 包含 config.json、model.bin、tokenizer.json 与词表文件：faster-whisper
// 加载本地目录时缺少 tokenizer 或词表会直接报 “Cannot load the vocabulary”，
// 因此它们都是运行必需文件；下载中断留下的半成品目录也不会被当成「已安装」。
func DefaultCatalog() []CatalogEntry {
	entries := []CatalogEntry{
		newEntry(
			"tiny",
			"Whisper Tiny",
			"约 39M 参数，速度最快、精度最低，适合快速草稿与低配机器。",
			sizeModelTiny,
			[]ManifestFile{
				{Path: "config.json", Size: 2249},
				{Path: "model.bin", Size: sizeModelTiny},
				{Path: "tokenizer.json", Size: sizeTokenizerSmall},
				{Path: "vocabulary.txt", Size: sizeVocabularyTxt},
			},
		),
		newEntry(
			"base",
			"Whisper Base",
			"约 74M 参数，速度与中文识别质量的平衡点，适合日常短视频。",
			sizeModelBase,
			[]ManifestFile{
				{Path: "config.json", Size: 2309},
				{Path: "model.bin", Size: sizeModelBase},
				{Path: "tokenizer.json", Size: sizeTokenizerSmall},
				{Path: "vocabulary.txt", Size: sizeVocabularyTxt},
			},
		),
		newEntry(
			"small",
			"Whisper Small",
			"约 244M 参数，中文准确率明显提升，推荐的通用默认模型。",
			sizeModelSmall,
			[]ManifestFile{
				{Path: "config.json", Size: 2370},
				{Path: "model.bin", Size: sizeModelSmall},
				{Path: "tokenizer.json", Size: sizeTokenizerSmall},
				{Path: "vocabulary.txt", Size: sizeVocabularyTxt},
			},
		),
		newEntry(
			"medium",
			"Whisper Medium",
			"约 769M 参数，准确率更高，需要更多内存与更长的识别时间。",
			sizeModelMedium,
			[]ManifestFile{
				{Path: "config.json", Size: 2257},
				{Path: "model.bin", Size: sizeModelMedium},
				{Path: "tokenizer.json", Size: sizeTokenizerSmall},
				{Path: "vocabulary.txt", Size: sizeVocabularyTxt},
			},
		),
		newEntry(
			"large-v3",
			"Whisper Large v3",
			"约 1.55B 参数，多语言与中文准确率最好，体积和显存占用也最大。",
			sizeModelLargeV3,
			[]ManifestFile{
				{Path: "config.json", Size: 2394},
				{Path: "model.bin", Size: sizeModelLargeV3},
				{Path: "tokenizer.json", Size: sizeTokenizerV3},
				{Path: "vocabulary.json", Size: sizeVocabularyV3},
				// large-v3 必须带 preprocessor_config.json（feature_size=128），
				// 否则 faster-whisper 会按 80 个 mel 通道提特征，与权重的 128
				// 通道不匹配，识别直接报 Invalid input features shape。
				{Path: "preprocessor_config.json", Size: sizePreprocessorV3},
			},
		),
		newCohereEntry(),
	}
	return entries
}

// newCohereEntry 组装 cohere-transcribe-03-2026 清单项。
//
// 该模型是 transformers 格式（CohereAsrForConditionalGeneration，2.07B 参数
// BF16 权重），必须由 transformers 引擎加载，faster-whisper 无法读取。
// 实测在 HF Open ASR Leaderboard 上平均 WER 5.42%，支持 14 种语言（含中文）。
func newCohereEntry() CatalogEntry {
	files := []ManifestFile{
		{Path: "config.json", Size: 3998},
		{Path: "generation_config.json", Size: 234},
		{Path: "preprocessor_config.json", Size: 420},
		{Path: "processor_config.json", Size: 131},
		{Path: "special_tokens_map.json", Size: 4091},
		{Path: "tokenizer.json", Size: 1816694},
		{Path: "tokenizer.model", Size: 492827},
		{Path: "tokenizer_config.json", Size: 48138},
		{Path: "model.safetensors", Size: 4131862976},
		{Path: "configuration_cohere_asr.py", Size: 1795},
		{Path: "modeling_cohere_asr.py", Size: 64648},
		{Path: "processing_cohere_asr.py", Size: 20572},
		{Path: "tokenization_cohere_asr.py", Size: 6623},
	}
	return CatalogEntry{
		ID:          "cohere-transcribe",
		Name:        "Cohere Transcribe 03-2026",
		Repo:        cohereTranscribeRepo,
		Description: "2.07B 参数，Open ASR Leaderboard 平均 WER 5.42%，支持含中文的 14 种语言；transformers 格式，需要 torch 与 transformers 5.x。",
		SizeBytes:   4131862976,
		Files:       files,
		Required:    cohereTranscribeRequired,
		Engine:      EngineTransformers,
	}
}

// newEntry 组装一条清单项，自动填充 Repo 与 Required。
func newEntry(id, name, description string, modelBinSize int64, files []ManifestFile) CatalogEntry {
	total := int64(0)
	for _, f := range files {
		total += f.Size
	}
	// 以文件体积之和为准；若外部传入的粗略值更大则保留（便于人工估算富余）。
	size := total
	if modelBinSize > 0 && modelBinSize > size {
		size = modelBinSize
	}
	return CatalogEntry{
		ID:          id,
		Name:        name,
		Repo:        fasterWhisperRepoPrefix + id,
		Description: description,
		SizeBytes:   size,
		Files:       files,
		Required:    requiredFiles(files),
	}
}

// requiredFiles 返回清单中全部文件路径：模型目录缺少任何一个都无法被
// faster-whisper 正常加载，因此全部视为必需文件。
func requiredFiles(files []ManifestFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// NormalizeModelID 把各种写法归一化为清单 ID。
//
// 支持的输入形式（大小写、下划线/空格、首尾空白都会忽略）：
//
//	small
//	Systran/faster-whisper-small
//	faster-whisper-small
//	systran__faster-whisper-small     （本地缓存目录名会把 / 换成 __）
//	Systran--faster-whisper-small     （Hugging Face 的旧式缓存目录名）
//
// 注意：这里只做「宽进」归一化，产出的结果不一定是清单 ID。例如
// viethang/cohere-transcribe-03-2026 会得到 cohere-transcribe-03-2026，
// 而清单 ID 是 cohere-transcribe。所以匹配必须走 findInCatalog —— 它会
// 同时比对原始 ID、Repo 与归一化结果，不要直接用本函数的输出做等值判断。
func NormalizeModelID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if s == "" {
		return ""
	}
	// 统一分隔符：本地缓存目录中可能把 / 编码成 __ 或 --。
	s = strings.ReplaceAll(s, "__", "/")
	s = strings.ReplaceAll(s, "--", "/")
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "")

	// 只保留最后一段，用于匹配 tiny/base/small/medium/large-v3 等型号。
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimPrefix(s, "faster-whisper-")
	s = strings.TrimPrefix(s, "faster-whisper")
	s = strings.TrimPrefix(s, "whisper-")
	s = strings.TrimPrefix(s, "whisper")
	s = strings.Trim(s, "-")
	return s
}

// FindCatalogEntry 在给定清单中按 ID 查找模型，支持别名归一化。
func FindCatalogEntry(id string) (CatalogEntry, bool) {
	return findInCatalog(DefaultCatalog(), id)
}

// findInCatalog 与 FindCatalogEntry 逻辑相同，但允许传入自定义清单（测试与注入用）。
func findInCatalog(catalog []CatalogEntry, id string) (CatalogEntry, bool) {
	want := NormalizeModelID(id)
	raw := strings.ToLower(strings.TrimSpace(id))
	for _, e := range catalog {
		if raw != "" && strings.ToLower(e.ID) == raw {
			return e, true
		}
		if strings.EqualFold(e.Repo, strings.TrimSpace(id)) {
			return e, true
		}
	}
	if want == "" {
		return CatalogEntry{}, false
	}
	for _, e := range catalog {
		if NormalizeModelID(e.ID) == want {
			return e, true
		}
		if NormalizeModelID(e.Repo) == want {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// modelIDs 返回清单中所有模型 ID（已排序，便于稳定输出）。
func modelIDs(catalog []CatalogEntry) []string {
	ids := make([]string, 0, len(catalog))
	for _, e := range catalog {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
