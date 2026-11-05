package media

import "context"

// 降噪预设名，与设置里的 NoiseReduction 取值一致。
const (
	// PresetOff 表示不做任何处理。
	PresetOff = "off"
	// PresetLight 是轻度降噪：去低频轰鸣与底噪。
	PresetLight = "light"
	// PresetStandard 是标准降噪：降噪 + 动态压缩 + 响度归一化。
	PresetStandard = "standard"
	// PresetStrong 是强力降噪：噪声环境适用。
	PresetStrong = "strong"
	// PresetCustom 表示使用用户自填的滤镜链。
	PresetCustom = "custom"
)

// EnhanceRequest 是音频增强（降噪）参数。
type EnhanceRequest struct {
	InputPath  string
	OutputPath string
	// Preset 是预设名：off/light/standard/strong/custom。空值按 off 处理。
	Preset string
	// FilterChain 是自定义滤镜链，仅 Preset == PresetCustom 时生效。
	FilterChain string
	// SampleRate / Channels 为输出参数；SampleRate<=0 用 16000，Channels<=0 用 1。
	SampleRate int
	Channels   int
}

// EnhanceResult 汇报实际生效的滤镜。
type EnhanceResult struct {
	// Applied 是实际生效的滤镜（已剔除本机 FFmpeg 不支持的项）。
	Applied []string
	// Skipped 是因能力缺失被剔除的滤镜。
	Skipped []string
	// Filter 是最终 -af 串；为空表示未做任何处理。
	Filter string
}

// Enhancer 是可对音频做降噪增强的实现。
//
// media 包提供 FFmpeg 实现；service 包依赖该接口以便测试注入。
type Enhancer interface {
	Enhance(ctx context.Context, req EnhanceRequest) (EnhanceResult, error)
}

// PresetFilters 返回预设对应的有序滤镜链。
//
// off / 未知预设返回 nil；custom 不在此处理（由调用方传入原始链）。
// 返回的是「期望链」，实际生效的链还要经过能力剔除。
func PresetFilters(preset string) []string {
	switch normalizePreset(preset) {
	case PresetLight:
		return []string{
			"highpass=f=80",
			"afftdn=nf=-30",
			"loudnorm=I=-16:TP=-1.5:LRA=11",
		}
	case PresetStandard:
		return []string{
			"highpass=f=80",
			"afftdn=nf=-25:nr=10",
			"acompressor=threshold=-20dB:ratio=4:attack=5:release=50",
			"loudnorm=I=-16:TP=-1.5:LRA=11",
		}
	case PresetStrong:
		return []string{
			"highpass=f=90",
			"afftdn=nf=-20:nr=15",
			"acompressor=threshold=-18dB:ratio=6:attack=5:release=80",
			"deesser=i=0.4:m=0.5:f=0.5:s=o",
			"loudnorm=I=-16:TP=-1.5:LRA=11",
		}
	default:
		return nil
	}
}

// normalizePreset 把预设名规范成小写，未知值回落到 off。
func normalizePreset(preset string) string {
	switch lower := trimLower(preset); lower {
	case PresetLight, PresetStandard, PresetStrong, PresetCustom:
		return lower
	default:
		return PresetOff
	}
}

// trimLower 是 strings.ToLower(strings.TrimSpace(...)) 的本地实现，
// 避免为一个小工具引入额外导入。
func trimLower(s string) string {
	out := make([]rune, 0, len(s))
	started := false
	pending := make([]rune, 0, len(s))
	for _, r := range s {
		if !started {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				continue
			}
			started = true
		}
		pending = append(pending, r)
	}
	// 去掉尾部空白
	for len(pending) > 0 {
		last := pending[len(pending)-1]
		if last == ' ' || last == '\t' || last == '\n' || last == '\r' {
			pending = pending[:len(pending)-1]
			continue
		}
		break
	}
	for _, r := range pending {
		if r >= 'A' && r <= 'Z' {
			r = r + ('a' - 'A')
		}
		out = append(out, r)
	}
	return string(out)
}
