package main

import (
	"context"
	"log"
	"strings"

	"transcribe-whisper/internal/media"
)

// FFmpeg 完全由用户手动指定，不做任何自动发现。
//
// 设计取舍：早期版本会自动扫描 PATH、常见安装目录甚至整盘寻找「能力最强」
// 的构建，看似省事，实际有三个问题：
//  1. 用户根本不知道自己机器上到底在用哪个 FFmpeg，出问题无从排查；
//  2. 深度扫描要遍历磁盘，启动时间不可控；
//  3. 便携工具包里的构建可能被意外「借用」，之后该工具被删除就突然失效。
//
// 现在改成：用户明确指定一个 ffmpeg.exe（或在设置页一键下载完整版）。
// 没配置就明确告诉用户「未安装」并给出下载入口，而不是悄悄挑一个。

// resolveFFmpeg 按用户配置构造 FFmpeg 封装。
//
// 未配置或配置的路径不可用时返回 nil —— 调用方必须把 nil 当成「未安装」
// 处理并提示用户，而不是静默降级。
func resolveFFmpeg(ctx context.Context, configured string) *media.FFmpeg {
	path := strings.TrimSpace(configured)
	if path == "" {
		return nil
	}
	ffmpeg, err := media.New(path)
	if err != nil {
		log.Printf("配置的 FFmpeg 路径不可用（%s）：%v", path, err)
		return nil
	}
	caps := ffmpeg.Available(ctx)
	if !caps.OK {
		log.Printf("配置的 FFmpeg 无法执行（%s）：%s", path, caps.Detail)
		return nil
	}
	if !caps.SupportsSubtitlesBurn {
		// 能用但没有 subtitles 滤镜：识别与提取仍然可用，只是不能压制字幕。
		// 这种情况保留实例，由界面提示用户换完整版。
		log.Printf("FFmpeg 缺少 subtitles 滤镜（%s），字幕压制不可用", path)
	}
	return ffmpeg
}

// ffmpegStatus 汇总 FFmpeg 的可用性，供界面判断是否需要提示安装。
//
// 判定依据全部来自「用户配置了什么」，不再有隐式发现：
//   - configured：用户是否填了路径；
//   - ok：配置的 FFmpeg 是否真的能跑；
//   - canBurn：是否支持字幕压制（需要完整版构建）。
type ffmpegStatus struct {
	Configured bool
	Path       string
	Version    string
	OK         bool
	CanBurn    bool
	Detail     string
}

// inspectFFmpeg 探测当前配置的 FFmpeg 状态。
func inspectFFmpeg(ctx context.Context, configured string) ffmpegStatus {
	status := ffmpegStatus{Configured: strings.TrimSpace(configured) != "", Path: strings.TrimSpace(configured)}
	if !status.Configured {
		status.Detail = "尚未指定 FFmpeg。音频提取、语音识别与字幕压制都需要它，请先下载或选择本机已有的 ffmpeg.exe。"
		return status
	}
	ffmpeg, err := media.New(status.Path)
	if err != nil {
		status.Detail = "指定的 FFmpeg 不可用：" + err.Error()
		return status
	}
	caps := ffmpeg.Available(ctx)
	status.OK = caps.OK
	status.Path = caps.Path
	status.Version = caps.Version
	status.CanBurn = caps.SupportsSubtitlesBurn
	if caps.OK {
		status.Detail = caps.Detail
	} else {
		status.Detail = caps.Detail
		if strings.TrimSpace(status.Detail) == "" {
			status.Detail = "指定的 FFmpeg 无法执行，请重新选择"
		}
	}
	return status
}

// applyFFmpegSettings 在 FFmpeg 路径变化后重建媒体依赖。
//
// 返回新的封装（未配置或不可用时为 nil）与状态。调用方负责把它写进
// a.media 并同步给任务队列。
func applyFFmpegSettings(ctx context.Context, configured string) (*media.FFmpeg, ffmpegStatus) {
	ffmpeg := resolveFFmpeg(ctx, configured)
	return ffmpeg, inspectFFmpeg(ctx, configured)
}
