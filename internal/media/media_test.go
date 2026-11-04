package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 说明：本文件所有测试都离线运行，不依赖真实媒体文件与真实 FFmpeg。
// Windows 下用临时 .bat 模拟 ffmpeg/ffprobe（cmd.exe 会按 PATHEXT 解析无扩展名命令），
// 其它平台用等价 sh 脚本；.bat 内不要写 `( ... & exit /b 0 )` 这类括号块，
// cmd 会把整行一次性解析，遇到第一行末尾的括号就会整体退出。

// writeScript 生成可直接执行的假命令脚本，返回脚本路径。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, name+".bat")
		if err := os.WriteFile(path, []byte("@echo off\r\n"+body), 0o644); err != nil {
			t.Fatalf("写入假命令脚本失败：%v", err)
		}
		return path
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("写入假命令脚本失败：%v", err)
	}
	return path
}

// writeFile 在临时目录写一个占位文件。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败：%v", err)
	}
	return path
}

// argsRecordWindows 生成「把参数逐行写入记录文件」的 bat 片段。
func argsRecord(dir, recordFile string) string {
	// for 默认用空格/逗号/分号/等号切分参数，这里改用 %* 原文记录再逐行拆分。
	return "echo %*>>\"" + recordFile + "\"\r\n"
}

const fakeVersionOutput = "ffmpeg version 7.1.1 Copyright (c) 2000-2025 the FFmpeg developers\n" +
	"built with clang version 21.1.2\n" +
	"configuration: --enable-gpl --enable-libx264\n"

const fakeFiltersWithSubtitles = `Filters:
  T.. = Timeline support
  ... ass               V->V       Render ASS subtitles libass: yes
  ... subtitles         V->V       Render text subtitles onto input video using the libass library.
 T.C drawtext          V->V       Draw text on top of video frames using libfreetype library.
  ... scale             V->V       Scale the input video size and/or convert the image format.
`

const fakeFiltersNoSubtitles = `Filters:
  T.. = Timeline support
  ... ass               V->V       Render ASS subtitles
 T.C drawtext          V->V       Draw text on top of video frames using libfreetype library.
  ... scale             V->V       Scale the input video size and/or convert the image format.
`

const fakeFiltersOnlyScale = `Filters:
  ... scale             V->V       Scale the input video size and/or convert the image format.
`

// batIf 生成「参数匹配则执行 then 分支」的 bat 片段。
// 注意不要用 goto :label，cmd 对标签解析依赖文件偏移，测试里不可靠。
func batIf(arg, want, then string) string {
	return "if \"" + arg + "\"==\"" + want + "\" (\r\n" + then + ")\r\n"
}

// fakeFFmpegWithCaps 生成一个假 ffmpeg：-version 输出版本，-filters 输出滤镜表。
// 返回假命令的真实路径（Windows 下带 .bat 扩展名）。
//
// 实现统一以「ffmpeg -hide_banner <子命令>」调用（与真实 ffmpeg 一致），
// 所以子命令是第二个参数 %2。
func fakeFFmpegWithCaps(t *testing.T, dir, versionFile, filtersFile, counterFile string) string {
	t.Helper()
	count := ""
	if counterFile != "" {
		count = "  echo x>>\"" + counterFile + "\"\r\n"
	}
	body := batIf("%2", "-version", "  type \""+versionFile+"\"\r\n"+count) +
		batIf("%2", "-filters", "  type \""+filtersFile+"\"\r\n"+count)
	return writeScript(t, dir, "ffmpeg", body)
}

func TestNewMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	_, err := New(filepath.Join(dir, "不存在", "ffmpeg.exe"))
	if err == nil {
		t.Fatal("期望路径不存在时返回错误，但得到 nil")
	}
	if !strings.Contains(err.Error(), "ffmpeg") || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("错误信息应为中文且包含 ffmpeg 关键字，实际：%v", err)
	}
}

func TestNewDirectoryPath(t *testing.T) {
	_, err := New(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("期望目录路径报错，实际：%v", err)
	}
}

func TestNewLookPathFailure(t *testing.T) {
	// PATH 中无 ffmpeg 时应返回中文错误，而不是 panic。
	t.Setenv("PATH", t.TempDir())
	_, err := New("")
	if !errors.Is(err, ErrFFmpegNotFound) {
		t.Fatalf("期望 ErrFFmpegNotFound，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "未找到 ffmpeg") {
		t.Fatalf("期望中文提示，实际：%v", err)
	}
}

func TestNewAcceptsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(path)
	if err != nil {
		t.Fatalf("显式路径应构造成功，实际：%v", err)
	}
	if filepath.Base(ff.Path()) != filepath.Base(path) {
		t.Fatalf("Path 返回不符：%s", ff.Path())
	}
}

func TestAvailableReportsSubtitlesCapability(t *testing.T) {
	cases := []struct {
		name      string
		filters   string
		wantSub   bool
		wantDraw  bool
		wantInMsg string
	}{
		{"含 subtitles 与 drawtext", fakeFiltersWithSubtitles, true, true, "支持 subtitles 滤镜"},
		{"缺少 subtitles（真实环境 7.1.1）", fakeFiltersNoSubtitles, false, true, "缺少 subtitles 滤镜"},
		{"两者都没有", fakeFiltersOnlyScale, false, false, "drawtext"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			versionFile := writeFile(t, dir, "version.txt", fakeVersionOutput)
			filtersFile := writeFile(t, dir, "filters.txt", tc.filters)
			fakeFFmpegWithCaps(t, dir, versionFile, filtersFile, "")

			ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			caps := ff.Available(context.Background())
			if !caps.OK {
				t.Fatalf("应检测为可用，实际 detail=%s", caps.Detail)
			}
			if caps.SupportsSubtitlesBurn != tc.wantSub {
				t.Fatalf("SupportsSubtitlesBurn 期望 %v，实际 %v", tc.wantSub, caps.SupportsSubtitlesBurn)
			}
			if caps.SupportsDrawText != tc.wantDraw {
				t.Fatalf("SupportsDrawText 期望 %v，实际 %v", tc.wantDraw, caps.SupportsDrawText)
			}
			if !strings.Contains(caps.Detail, tc.wantInMsg) {
				t.Fatalf("Detail 应包含 %q，实际：%s", tc.wantInMsg, caps.Detail)
			}
			if !strings.Contains(caps.Version, "7.1.1") {
				t.Fatalf("版本号解析错误：%q", caps.Version)
			}
			if caps.Path == "" {
				t.Fatal("Path 不应为空")
			}
		})
	}
}

func TestAvailableCachesResult(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count.txt")
	versionFile := writeFile(t, dir, "version.txt", fakeVersionOutput)
	filtersFile := writeFile(t, dir, "filters.txt", fakeFiltersNoSubtitles)
	fakeFFmpegWithCaps(t, dir, versionFile, filtersFile, counter)

	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	for i := 0; i < 3; i++ {
		ff.Available(context.Background())
	}
	data, _ := os.ReadFile(counter)
	// 首次探测执行 2 次命令（-version 与 -filters），之后命中缓存。
	if lines := len(strings.Fields(string(data))); lines != 2 {
		t.Fatalf("期望命令只执行 2 次，实际 %d 次", lines)
	}

	// 刷新缓存后应重新探测。
	ff.RefreshCapabilities(context.Background())
	data, _ = os.ReadFile(counter)
	if lines := len(strings.Fields(string(data))); lines != 4 {
		t.Fatalf("刷新后期望累计 4 次，实际 %d 次", lines)
	}
}

func TestAvailableReportsFailure(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeScript(t, dir, "ffmpeg", "echo ffmpeg: shared library load failed 1>&2\r\nexit /b 1\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	caps := ff.Available(context.Background())
	if caps.OK {
		t.Fatal("执行失败时 OK 必须为 false")
	}
	if !strings.Contains(caps.Detail, "FFmpeg") {
		t.Fatalf("Detail 应说明失败原因，实际：%s", caps.Detail)
	}
}

const fakeProbeJSON = `{
  "streams": [
    {"codec_type":"video","codec_name":"h264","width":1920,"height":1080},
    {"codec_type":"video","codec_name":"mjpeg","width":300,"height":300,"disposition":{"attached_pic":1}},
    {"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2}
  ],
  "format": {"format_name":"mov,mp4","duration":"123.456","bit_rate":"1280000","size":"1024"}
}`

func TestProbeParsesFFprobeJSON(t *testing.T) {
	dir := t.TempDir()
	mediaFile := writeFile(t, dir, "input.mp4", "fake")
	jsonFile := writeFile(t, dir, "probe.json", fakeProbeJSON)
	recordFile := filepath.Join(dir, "args.txt")

	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	body := argsRecord(dir, recordFile) + "type \"" + jsonFile + "\"\r\n" + "exit /b 0\r\n"
	if runtime.GOOS != "windows" {
		body = "for a in \"$@\"; do echo \"$a\" >> '" + recordFile + "'; done\ncat '" + jsonFile + "'\n"
	}
	_ = writeScript(t, dir, "ffprobe", body)

	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if ff.FFprobePath() == "" {
		t.Fatal("同目录假 ffprobe 应被识别")
	}

	info, err := ff.Probe(context.Background(), mediaFile)
	if err != nil {
		t.Fatalf("Probe 不应失败：%v", err)
	}
	if info.Duration != 123.456 {
		t.Fatalf("时长解析错误：%v", info.Duration)
	}
	if info.BitRate != 1280000 {
		t.Fatalf("码率解析错误：%d", info.BitRate)
	}
	if info.FormatName != "mov,mp4" {
		t.Fatalf("容器格式解析错误：%q", info.FormatName)
	}
	if !info.HasVideo || info.VideoCodec != "h264" || info.Width != 1920 || info.Height != 1080 {
		t.Fatalf("视频流解析错误（封面图应被跳过）：%+v", info)
	}
	if !info.HasAudio || info.AudioCodec != "aac" || info.SampleRate != 48000 || info.Channels != 2 {
		t.Fatalf("音频流解析错误：%+v", info)
	}

	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{"-print_format", "json", "-show_format", "-show_streams", "-v", "error", mediaFile} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ffprobe 参数缺少 %q，实际：%s", want, joined)
		}
	}
}

func TestProbeFileMissing(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	_, err = ff.Probe(context.Background(), filepath.Join(dir, "不存在.mp4"))
	if err == nil {
		t.Fatal("文件不存在时应返回错误")
	}
	if !strings.Contains(err.Error(), "媒体文件不存在") {
		t.Fatalf("错误信息应为明确中文，实际：%v", err)
	}
}

func TestProbeRejectsDirectoryAndEmptyFile(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if _, err := ff.Probe(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("目录路径应报错，实际：%v", err)
	}
	empty := writeFile(t, dir, "empty.mp4", "")
	if _, err := ff.Probe(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空文件应报错，实际：%v", err)
	}
	if _, err := ff.Probe(context.Background(), ""); err == nil {
		t.Fatal("空路径应报错")
	}
}

func TestProbeWithoutFFprobeDegrades(t *testing.T) {
	dir := t.TempDir()
	mediaFile := writeFile(t, dir, "input.mp3", "fake")
	// 假 ffmpeg 只在 stderr 打印流信息，模拟真实 ffmpeg -i 的行为并返回非 0。
	writeScript(t, dir, "ffmpeg",
		"echo Stream #0:0(und): Audio: mp3, 44100 Hz, stereo, fltp, 128 kb/s 1>&2\r\nexit /b 1\r\n")

	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	// 排除 PATH 中的真实 ffprobe，强制走降级分支。
	t.Setenv("PATH", "")
	ff.ffprobePath = ""

	info, err := ff.Probe(context.Background(), mediaFile)
	if err == nil {
		t.Fatal("缺少 ffprobe 时应返回说明性错误")
	}
	if !strings.Contains(err.Error(), "未找到 ffprobe") {
		t.Fatalf("错误应说明缺少 ffprobe，实际：%v", err)
	}
	if info.Duration != 0 {
		t.Fatalf("降级时 Duration 必须为 0，实际：%v", info.Duration)
	}
	if !info.HasAudio || info.HasVideo {
		t.Fatalf("降级时仍应只识别出音频流，实际：%+v", info)
	}
}

func TestExtractCommandArgs(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.mp4", "fake")
	recordFile := filepath.Join(dir, "args.txt")
	output := filepath.Join(dir, "out", "nested", "audio.wav")

	body := argsRecord(dir, recordFile) + "echo wav>\"" + output + "\"\r\n" + "exit /b 0\r\n"
	if runtime.GOOS != "windows" {
		body = "for a in \"$@\"; do echo \"$a\" >> '" + recordFile + "'; done\n" +
			"mkdir -p \"$(dirname '$output')\"; echo wav > '" + output + "'\n"
	}
	ffmpegPath := writeScript(t, dir, "ffmpeg", body)

	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	// 父目录不存在时应自动创建。
	if err := ff.Extract(context.Background(), ExtractRequest{InputPath: input, OutputPath: output}); err != nil {
		t.Fatalf("Extract 不应失败：%v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("输出文件未生成：%v", err)
	}

	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{"-i " + input, "-vn", "-ac 1", "-ar 16000", "-acodec pcm_s16le", output} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少参数 %q，实际：%s", want, joined)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(joined), "-y") {
		t.Fatalf("应以 -y 开头，实际：%s", joined)
	}
}

func TestExtractCustomSampleRateAndChannels(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.mp4", "fake")
	recordFile := filepath.Join(dir, "args.txt")
	output := filepath.Join(dir, "out.wav")
	body := argsRecord(dir, recordFile) + "echo wav>\"" + output + "\"\r\n" + "exit /b 0\r\n"
	if runtime.GOOS != "windows" {
		body = "for a in \"$@\"; do echo \"$a\" >> '" + recordFile + "'; done\necho wav > '" + output + "'\n"
	}
	ffmpegPath := writeScript(t, dir, "ffmpeg", body)
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if err := ff.Extract(context.Background(), ExtractRequest{
		InputPath: input, OutputPath: output, SampleRate: 44100, Channels: 2,
	}); err != nil {
		t.Fatalf("Extract 不应失败：%v", err)
	}
	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	if !strings.Contains(joined, "-ar 44100") || !strings.Contains(joined, "-ac 2") {
		t.Fatalf("自定义参数未生效：%s", joined)
	}
}

func TestExtractArgsHelperDefaults(t *testing.T) {
	args := extractArgs("in.mp4", "out.wav", defaultSampleRate, defaultChannels)
	want := []string{"-y", "-hide_banner", "-nostdin", "-i", "in.mp4", "-vn",
		"-ac", "1", "-ar", "16000", "-acodec", "pcm_s16le", "out.wav"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("参数拼装不符：\n得到 %v\n期望 %v", args, want)
	}
}

func TestExtractInvalidInput(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if err := ff.Extract(context.Background(), ExtractRequest{}); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空路径应报中文错误，实际：%v", err)
	}
	err = ff.Extract(context.Background(), ExtractRequest{
		InputPath:  filepath.Join(dir, "missing.mp4"),
		OutputPath: filepath.Join(dir, "out.wav"),
	})
	if err == nil || !strings.Contains(err.Error(), "输入文件不存在") {
		t.Fatalf("输入不存在应报中文错误，实际：%v", err)
	}
	if err := ff.Extract(context.Background(), ExtractRequest{InputPath: dir, OutputPath: filepath.Join(dir, "o.wav")}); err == nil ||
		!strings.Contains(err.Error(), "目录") {
		t.Fatalf("目录输入应报错，实际：%v", err)
	}
}

func TestExtractErrorMessageIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.mp4", "fake")
	output := filepath.Join(dir, "out.wav")
	writeScript(t, dir, "ffmpeg",
		"echo [in#0] Error opening input: Invalid data found when processing input 1>&2\r\nexit /b 1\r\n")
	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	err = ff.Extract(context.Background(), ExtractRequest{InputPath: input, OutputPath: output})
	if err == nil {
		t.Fatal("ffmpeg 失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "音频提取失败") {
		t.Fatalf("错误应为中文前缀，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("错误应包含 stderr 关键片段，实际：%v", err)
	}
}

func TestExtractFailsWhenNoOutputProduced(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.mp4", "fake")
	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	err = ff.Extract(context.Background(), ExtractRequest{InputPath: input, OutputPath: filepath.Join(dir, "out.wav")})
	if err == nil || !strings.Contains(err.Error(), "未生成有效输出文件") {
		t.Fatalf("未产出文件时应报错，实际：%v", err)
	}
}

func TestBurnRejectsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeScript(t, dir, "ffmpeg", "exit /b 0\r\n")
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	video := writeFile(t, dir, "v.mp4", "fake")
	err = ff.Burn(context.Background(), BurnRequest{
		VideoPath:    video,
		SubtitlePath: filepath.Join(dir, "无.srt"),
		OutputPath:   filepath.Join(dir, "out.mp4"),
	})
	if err == nil || !strings.Contains(err.Error(), "字幕文件不存在") {
		t.Fatalf("字幕缺失应报中文错误，实际：%v", err)
	}
	if err := ff.Burn(context.Background(), BurnRequest{}); err == nil {
		t.Fatal("空参数应报错")
	}
}

func TestBurnDegradesWithoutSubtitlesFilter(t *testing.T) {
	dir := t.TempDir()
	video := writeFile(t, dir, "v.mp4", "fake")
	sub := writeFile(t, dir, "v.srt", "1\n00:00:00,000 --> 00:00:01,000\nhello\n")
	output := filepath.Join(dir, "out.mp4")

	versionFile := writeFile(t, dir, "version.txt", fakeVersionOutput)
	filtersFile := writeFile(t, dir, "filters.txt", fakeFiltersNoSubtitles)
	fakeFFmpegWithCaps(t, dir, versionFile, filtersFile, "")

	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	err = ff.Burn(context.Background(), BurnRequest{VideoPath: video, SubtitlePath: sub, OutputPath: output})
	if err == nil {
		t.Fatal("缺少 subtitles 滤镜时必须返回错误，不能假装成功")
	}
	if !strings.Contains(err.Error(), "当前 FFmpeg 未编译 libass，无法压制字幕（缺少 subtitles 滤镜）") {
		t.Fatalf("错误文案不符合要求，实际：%v", err)
	}
	if !errors.Is(err, ErrSubtitlesUnsupported) {
		t.Fatalf("应可通过 errors.Is 判断为 ErrSubtitlesUnsupported，实际：%v", err)
	}
	if stat, statErr := os.Stat(output); statErr == nil && stat.Size() > 0 {
		t.Fatal("降级时不应产出文件")
	}
}

func TestBurnRunsWithSubtitlesFilter(t *testing.T) {
	dir := t.TempDir()
	video := writeFile(t, dir, "v.mp4", "fake")
	sub := writeFile(t, dir, "v.srt", "1\n")
	output := filepath.Join(dir, "out.mp4")
	recordFile := filepath.Join(dir, "args.txt")

	versionFile := writeFile(t, dir, "version.txt", fakeVersionOutput)
	filtersFile := writeFile(t, dir, "filters.txt", fakeFiltersWithSubtitles)

	body := batIf("%2", "-version", "  type \""+versionFile+"\"\r\n") +
		batIf("%2", "-filters", "  type \""+filtersFile+"\"\r\n") +
		argsRecord(dir, recordFile) +
		"echo x>\"" + output + "\"\r\n"
	if runtime.GOOS != "windows" {
		body = "if [ \"$1\" = \"-version\" ]; then cat '" + versionFile + "'; exit 0; fi\n" +
			"if [ \"$1\" = \"-filters\" ]; then cat '" + filtersFile + "'; exit 0; fi\n" +
			"for a in \"$@\"; do echo \"$a\" >> '" + recordFile + "'; done\n" +
			"echo x > '" + output + "'\n"
	}
	ffmpegPath := writeScript(t, dir, "ffmpeg", body)

	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	err = ff.Burn(context.Background(), BurnRequest{
		VideoPath: video, SubtitlePath: sub, OutputPath: output,
		FontName: "微软雅黑", FontSize: 28, MarginV: 40,
	})
	if err != nil {
		t.Fatalf("具备 subtitles 滤镜时不应失败：%v", err)
	}
	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{"-vf", "subtitles=", "force_style=", "FontSize=28", "MarginV=40", output} {
		if !strings.Contains(joined, want) {
			t.Fatalf("压制参数缺少 %q，实际：%s", want, joined)
		}
	}
	if !strings.Contains(joined, "-c:a copy") {
		t.Fatalf("音频应直接复制：%s", joined)
	}
}

func TestBurnArgsEscapesWindowsPath(t *testing.T) {
	args := burnArgs(`C:\视频\v.mp4`, `C:\视频\字幕 v2's.srt`, `C:\输出\out.mp4`,
		BurnRequest{FontSize: 24, MarginV: 10})
	var vf string
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) {
			vf = args[i+1]
		}
	}
	if vf == "" {
		t.Fatalf("未找到 -vf 参数：%v", args)
	}
	// 期望形如：subtitles='C\:/视频/字幕 v2\'s.srt':force_style='...'
	//
	// 转义级别以真实 FFmpeg 实测为准（gyan 8.1 full，见 TestRealBurnSubtitle）：
	// 滤镜串整体已用单引号包住，内部只需一级转义。早期实现的两级转义会
	// 生成 C\\:/，实测让滤镜图解析失败，FFmpeg 报出误导性的
	// “Error opening output file ... Invalid argument”。
	if !strings.HasPrefix(vf, "subtitles='") {
		t.Fatalf("路径应放在单引号内：%s", vf)
	}
	if !strings.Contains(vf, `C\:/`) {
		t.Fatalf("盘符冒号必须转义：%s", vf)
	}
	if strings.Contains(vf, `C\\`) {
		t.Fatalf("引号内不应出现两级转义（实测会让 FFmpeg 报 Invalid argument）：%s", vf)
	}
	if strings.Contains(vf, `C:\`) {
		t.Fatalf("Windows 反斜杠应换成斜杠：%s", vf)
	}
	if !strings.Contains(vf, `\'s`) {
		t.Fatalf("单引号必须转义：%s", vf)
	}
	if !strings.Contains(vf, "视频/字幕 v2") {
		t.Fatalf("中文路径不应被破坏：%s", vf)
	}
}

func TestEscapeFilterPath(t *testing.T) {
	got := escapeFilterPath(`C:\a b\字's.srt`)
	if !strings.Contains(got, `C\:/`) {
		t.Fatalf("冒号未转义：%s", got)
	}
	if !strings.Contains(got, "a b") {
		t.Fatalf("空格不应被改动：%s", got)
	}
	if strings.Contains(got, `\\`) {
		t.Fatalf("单级转义下不应出现成对反斜杠（两级转义实测不可用）：%s", got)
	}
}

func TestTruncateChars(t *testing.T) {
	s := strings.Repeat("错", 600)
	got := truncateChars(s, stderrLimit)
	if len([]rune(got)) > stderrLimit+1 {
		t.Fatalf("截断超出限制：%d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("截断后应带省略号")
	}
	if truncateChars("短", 10) != "短" {
		t.Fatal("未超长时不应改动")
	}
	if truncateChars("任意", 0) != "" {
		t.Fatal("limit 为 0 时应返回空串")
	}
}

func TestContextCancelKillsProcess(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.mp4", "fake")
	var ffmpegPath string
	if runtime.GOOS == "windows" {
		// ping 在 Windows 上可用于阻塞等待，保证子进程不会立刻退出。
		ffmpegPath = writeScript(t, dir, "ffmpeg", "ping -n 30 127.0.0.1 >nul\r\nexit /b 0\r\n")
	} else {
		ffmpegPath = writeScript(t, dir, "ffmpeg", "sleep 30\n")
	}
	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = ff.Extract(ctx, ExtractRequest{InputPath: input, OutputPath: filepath.Join(dir, "out.wav")})
	if err == nil {
		t.Fatal("取消后应返回错误")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Fatalf("取消错误应为中文说明，实际：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("取消未及时终止进程，耗时 %v", elapsed)
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var ff *FFmpeg
	if caps := ff.Available(context.Background()); caps.OK {
		t.Fatal("nil 接收者应返回不可用")
	}
	if err := ff.Extract(context.Background(), ExtractRequest{InputPath: "a", OutputPath: "b"}); err == nil {
		t.Fatal("nil 接收者应返回错误")
	}
	if err := ff.Burn(context.Background(), BurnRequest{VideoPath: "a", SubtitlePath: "b", OutputPath: "c"}); err == nil {
		t.Fatal("nil 接收者应返回错误")
	}
	if _, err := ff.Probe(context.Background(), "a"); err == nil {
		t.Fatal("nil 接收者应返回错误")
	}
	if err := (Burner)(ff).Burn(context.Background(), BurnRequest{}); err == nil {
		t.Fatal("通过接口调用 nil 接收者也应返回错误")
	}
}
