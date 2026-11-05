package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 编译期断言：FFmpeg 同时满足降噪增强接口。
var _ Enhancer = (*FFmpeg)(nil)

// 滤镜与能力位的对应关系（用于按本机 FFmpeg 能力剔除滤镜）：
//
//	afftdn      -> Capabilities.SupportsDenoise     （FFmpeg 4.1+）
//	deesser     -> Capabilities.SupportsDeesser     （FFmpeg 4.2+）
//	loudnorm    -> Capabilities.SupportsLoudnorm    （FFmpeg 3.1+）
//	acompressor -> Capabilities.SupportsCompressor  （FFmpeg 3.1+）
//	highpass 等 -> 恒可用（FFmpeg 2.x 起），一律保留
//
// 剔除而不是整条失败：降级后仍能做部分处理，比直接报错对用户更友好。

// Enhance 按预设或自定义滤镜链对音频做降噪增强（满足 Enhancer 接口）。
//
// 语义要点：
//   - 预设为 off（含空值与未知值）、或 custom 但滤镜链为空：不执行 FFmpeg，
//     返回零值结果与 nil 错误，调用方据 Filter == "" 判断无需替换文件。
//   - custom 链原样透传，不做能力剔除。
//   - 其余预设按本机能力剔除不支持的滤镜；剔除后链为空时降级为不处理（不报错）。
//   - 输出统一为 -ar <SampleRate> -ac <Channels> -acodec pcm_s16le 的 WAV。
func (f *FFmpeg) Enhance(ctx context.Context, req EnhanceRequest) (EnhanceResult, error) {
	if f == nil {
		return EnhanceResult{}, ErrFFmpegNotFound
	}

	preset := normalizePreset(req.Preset)
	if preset == PresetOff {
		return EnhanceResult{}, nil
	}

	var (
		applied []string
		skipped []string
		filter  string
	)
	if preset == PresetCustom {
		chain := strings.TrimSpace(req.FilterChain)
		if chain == "" {
			// 自定义模式但没填滤镜链：等价于不处理。
			return EnhanceResult{}, nil
		}
		// 自定义链交给用户负责，原样透传，不做能力剔除。
		filter = chain
		applied = splitFilterChain(chain)
	} else {
		caps := f.Available(ctx)
		applied, skipped = selectFilters(PresetFilters(preset), caps)
		filter = strings.Join(applied, ",")
	}

	if strings.TrimSpace(filter) == "" {
		// 所有滤镜都因能力缺失被剔除：降级为不处理，不报错。
		return EnhanceResult{Applied: applied, Skipped: skipped, Filter: ""}, nil
	}

	input := strings.TrimSpace(req.InputPath)
	output := strings.TrimSpace(req.OutputPath)
	if input == "" || output == "" {
		return EnhanceResult{}, errors.New("音频降噪失败：输入路径与输出路径都不能为空")
	}
	if err := checkEnhanceInput(input); err != nil {
		return EnhanceResult{}, err
	}

	sampleRate := req.SampleRate
	if sampleRate <= 0 {
		sampleRate = defaultSampleRate
	}
	channels := req.Channels
	if channels <= 0 {
		channels = defaultChannels
	}

	if dir := filepath.Dir(output); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return EnhanceResult{}, fmt.Errorf("创建输出目录 %s 失败：%v", dir, err)
		}
	}

	if _, err := f.run(ctx, enhanceArgs(input, output, filter, sampleRate, channels)...); err != nil {
		// 执行失败时返回零值结果，避免调用方误以为已产出可用文件；
		// 滤镜链放进错误信息里，便于日志定位。
		return EnhanceResult{}, fmt.Errorf("音频降噪失败（滤镜链：%s）：%v", filter, err)
	}
	if stat, err := os.Stat(output); err != nil || stat.Size() == 0 {
		return EnhanceResult{}, fmt.Errorf("音频降噪失败：FFmpeg 未生成有效输出文件 %s（请确认源文件含有音频轨）", output)
	}

	return EnhanceResult{Applied: applied, Skipped: skipped, Filter: filter}, nil
}

// checkEnhanceInput 校验输入文件存在且是普通文件，错误信息为中文。
func checkEnhanceInput(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("音频降噪失败：输入文件不存在：%s", path)
		}
		return fmt.Errorf("音频降噪失败：无法访问输入文件 %s：%v", path, err)
	}
	if stat.IsDir() {
		return fmt.Errorf("音频降噪失败：输入路径是目录而不是文件：%s", path)
	}
	return nil
}

// enhanceArgs 拼装降噪增强参数，单独抽出便于单元测试。
func enhanceArgs(input, output, filter string, sampleRate, channels int) []string {
	return []string{
		"-y",
		"-hide_banner",
		"-nostdin",
		"-i", input,
		"-vn",
		"-af", filter,
		"-ac", strconv.Itoa(channels),
		"-ar", strconv.Itoa(sampleRate),
		"-acodec", "pcm_s16le",
		output,
	}
}

// selectFilters 按本机能力剔除不支持的滤镜。
//
// 返回实际生效的链与被剔除的链；两者都保持原有顺序。
func selectFilters(filters []string, caps Capabilities) (applied, skipped []string) {
	for _, filter := range filters {
		filter = strings.TrimSpace(filter)
		if filter == "" {
			continue
		}
		if filterSupported(filter, caps) {
			applied = append(applied, filter)
			continue
		}
		skipped = append(skipped, filter)
	}
	return applied, skipped
}

// filterSupported 判断单个滤镜在当前 FFmpeg 上是否可用。
//
// 未列入能力映射的滤镜（highpass 等）视为恒可用，直接保留。
func filterSupported(filter string, caps Capabilities) bool {
	switch filterName(filter) {
	case "afftdn":
		return caps.SupportsDenoise
	case "deesser":
		return caps.SupportsDeesser
	case "loudnorm":
		return caps.SupportsLoudnorm
	case "acompressor":
		return caps.SupportsCompressor
	default:
		return true
	}
}

// filterName 从「滤镜名=参数」形式的串中取出滤镜名（小写）。
func filterName(filter string) string {
	name := strings.TrimSpace(filter)
	if idx := strings.IndexAny(name, "=,"); idx >= 0 {
		name = name[:idx]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// splitFilterChain 把逗号分隔的滤镜链拆成单项，用于展示「实际生效的滤镜」。
//
// 只用于结果汇报；真正传给 -af 的仍是调用方给出的原始链。
func splitFilterChain(chain string) []string {
	parts := strings.Split(chain, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
