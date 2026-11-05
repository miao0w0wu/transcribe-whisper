// Package media 封装 FFmpeg 探测、媒体元数据读取、音频提取与字幕压制。
//
// 该文件是包内共享契约，由 Lead 维护；实现位于同包其他文件。
package media

import "context"

// Info 是探测到的媒体元数据。
type Info struct {
	Path       string  `json:"path"`
	Duration   float64 `json:"duration"`
	BitRate    int64   `json:"bitRate"`
	FormatName string  `json:"formatName"`
	HasVideo   bool    `json:"hasVideo"`
	HasAudio   bool    `json:"hasAudio"`
	VideoCodec string  `json:"videoCodec"`
	AudioCodec string  `json:"audioCodec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	SampleRate int     `json:"sampleRate"`
	Channels   int     `json:"channels"`
}

// Capabilities 描述本机 FFmpeg 能力。
type Capabilities struct {
	Path                  string `json:"path"`
	Version               string `json:"version"`
	OK                    bool   `json:"ok"`
	SupportsSubtitlesBurn bool   `json:"supportsSubtitlesBurn"`
	SupportsDrawText      bool   `json:"supportsDrawText"`
	// 以下能力位用于音频降噪：缺失时对应滤镜会被剔除，降级而不是整条失败。
	SupportsDenoise    bool   `json:"supportsDenoise"`
	SupportsDeesser    bool   `json:"supportsDeesser"`
	SupportsLoudnorm   bool   `json:"supportsLoudnorm"`
	SupportsCompressor bool   `json:"supportsCompressor"`
	Detail             string `json:"detail"`
}

// ExtractRequest 是音频提取参数。
type ExtractRequest struct {
	InputPath  string
	OutputPath string
	SampleRate int
	Channels   int
}

// BurnRequest 是字幕压制参数。
type BurnRequest struct {
	VideoPath    string
	SubtitlePath string
	OutputPath   string
	FontName     string
	FontSize     int
	MarginV      int
}

// Burner 是可以把字幕烧进视频的实现。media 包提供 ffmpeg 实现，
// service 包依赖该接口以便测试注入。
type Burner interface {
	Burn(ctx context.Context, req BurnRequest) error
}

// EnhanceRequest、EnhanceResult、Enhancer 与降噪预设见 enhance_types.go。
