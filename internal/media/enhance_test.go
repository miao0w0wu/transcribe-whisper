package media

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 说明：本文件沿用 media_test.go 的离线测试风格，不依赖真实 FFmpeg。
// 需要真实执行 Enhance 的用例都用临时 .bat / sh 脚本模拟 ffmpeg。

// 全部音频滤镜都齐全的滤镜表（含降噪相关滤镜）。
const fakeFiltersWithAudio = `Filters:
  ... afftdn            A->A       Denoise audio samples using FFT.
  ... acompressor       A->A       Audio compressor.
  ... deesser           A->A       Apply de-essing to the audio samples.
  ... loudnorm          A->A       EBU R128 loudness normalization.
  ... highpass          A->A       Apply a high-pass filter with 3dB point frequency.
  T.C subtitles         V->V       Render text subtitles onto input video using the libass library.
`

// 缺少 afftdn / deesser 的滤镜表（模拟 FFmpeg 4.0 一类的旧版本）。
const fakeFiltersWithoutAudio = `Filters:
  ... acompressor       A->A       Audio compressor.
  ... loudnorm          A->A       EBU R128 loudness normalization.
  ... highpass          A->A       Apply a high-pass filter with 3dB point frequency.
`

// 只支持 highpass 的滤镜表：预设链会被整体剔除，用于验证降级不报错。
const fakeFiltersOnlyHighpass = `Filters:
  ... highpass          A->A       Apply a high-pass filter with 3dB point frequency.
`

// newFFmpegWithFilters 构造一个能力探测返回指定滤镜表的假 FFmpeg。
func newFFmpegWithFilters(t *testing.T, dir, filters string) *FFmpeg {
	t.Helper()
	versionFile := writeFile(t, dir, "version.txt", fakeVersionOutput)
	filtersFile := writeFile(t, dir, "filters.txt", filters)
	fakeFFmpegWithCaps(t, dir, versionFile, filtersFile, "")
	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	return ff
}

func TestDetectCapabilitiesReportsAudioFilters(t *testing.T) {
	cases := []struct {
		name               string
		filters            string
		wantDenoise        bool
		wantDeesser        bool
		wantLoudnorm       bool
		wantCompressor     bool
		wantDetailContains string
	}{
		{
			name:           "滤镜齐全",
			filters:        fakeFiltersWithAudio,
			wantDenoise:    true,
			wantDeesser:    true,
			wantLoudnorm:   true,
			wantCompressor: true,
		},
		{
			name:               "缺 afftdn 与 deesser",
			filters:            fakeFiltersWithoutAudio,
			wantDenoise:        false,
			wantDeesser:        false,
			wantLoudnorm:       true,
			wantCompressor:     true,
			wantDetailContains: "缺少 afftdn 滤镜",
		},
		{
			name:               "只有 highpass",
			filters:            fakeFiltersOnlyHighpass,
			wantDenoise:        false,
			wantDeesser:        false,
			wantLoudnorm:       false,
			wantCompressor:     false,
			wantDetailContains: "缺少 afftdn 滤镜",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ff := newFFmpegWithFilters(t, t.TempDir(), tc.filters)
			caps := ff.Available(context.Background())
			if !caps.OK {
				t.Fatalf("应检测为可用，实际 detail=%s", caps.Detail)
			}
			if caps.SupportsDenoise != tc.wantDenoise {
				t.Fatalf("SupportsDenoise 期望 %v，实际 %v", tc.wantDenoise, caps.SupportsDenoise)
			}
			if caps.SupportsDeesser != tc.wantDeesser {
				t.Fatalf("SupportsDeesser 期望 %v，实际 %v", tc.wantDeesser, caps.SupportsDeesser)
			}
			if caps.SupportsLoudnorm != tc.wantLoudnorm {
				t.Fatalf("SupportsLoudnorm 期望 %v，实际 %v", tc.wantLoudnorm, caps.SupportsLoudnorm)
			}
			if caps.SupportsCompressor != tc.wantCompressor {
				t.Fatalf("SupportsCompressor 期望 %v，实际 %v", tc.wantCompressor, caps.SupportsCompressor)
			}
			if tc.wantDetailContains != "" && !strings.Contains(caps.Detail, tc.wantDetailContains) {
				t.Fatalf("Detail 应包含 %q，实际：%s", tc.wantDetailContains, caps.Detail)
			}
		})
	}
}

// fakeFFmpegEnhance 生成「不探测能力、只记录参数并产出输出文件」的假 ffmpeg，
// 用于验证 Enhance 真正执行时的参数拼装。
func fakeFFmpegEnhance(t *testing.T, dir, recordFile, output string) string {
	t.Helper()
	body := argsRecord(dir, recordFile) + "echo wav>\"" + output + "\"\r\n"
	if runtime.GOOS != "windows" {
		body = "for a in \"$@\"; do echo \"$a\" >> '" + recordFile + "'; done\n" +
			"echo wav > '" + output + "'\n"
	}
	return writeScript(t, dir, "ffmpeg", body)
}

func TestEnhanceOffDoesNotRunFFmpeg(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	output := filepath.Join(dir, "out.wav")

	// ffmpeg 路径故意指向不存在的文件：一旦被调用就会失败。
	ff, err := New(writeScript(t, dir, "ffmpeg", "exit /b 0\r\n"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	ff.ffmpegPath = filepath.Join(dir, "不存在的-ffmpeg")

	for _, preset := range []string{"", "off", "OFF", "  off  ", "未知预设"} {
		res, err := ff.Enhance(context.Background(), EnhanceRequest{
			InputPath: input, OutputPath: output, Preset: preset,
		})
		if err != nil {
			t.Fatalf("preset=%q 时不应返回错误：%v", preset, err)
		}
		if res.Filter != "" || len(res.Applied) != 0 || len(res.Skipped) != 0 {
			t.Fatalf("preset=%q 时应返回零值结果，实际：%+v", preset, res)
		}
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("off 时不应产出任何文件")
	}
}

func TestEnhanceCustomEmptyChainDoesNotRunFFmpeg(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")

	ff, err := New(writeScript(t, dir, "ffmpeg", "exit /b 0\r\n"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	ff.ffmpegPath = filepath.Join(dir, "不存在的-ffmpeg")

	for _, chain := range []string{"", "   ", "\t\n"} {
		res, err := ff.Enhance(context.Background(), EnhanceRequest{
			InputPath: input, OutputPath: filepath.Join(dir, "out.wav"),
			Preset: PresetCustom, FilterChain: chain,
		})
		if err != nil {
			t.Fatalf("自定义链为空时不应报错：%v", err)
		}
		if res.Filter != "" {
			t.Fatalf("自定义链为空时应返回空 Filter，实际：%q", res.Filter)
		}
	}
}

func TestEnhanceCustomChainPassesThrough(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	output := filepath.Join(dir, "out.wav")
	recordFile := filepath.Join(dir, "args.txt")
	ffmpegPath := fakeFFmpegEnhance(t, dir, recordFile, output)

	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	chain := "highpass=f=100,afftdn=nf=-30,volume=2dB"
	res, err := ff.Enhance(context.Background(), EnhanceRequest{
		InputPath: input, OutputPath: output, Preset: PresetCustom, FilterChain: chain,
	})
	if err != nil {
		t.Fatalf("自定义链应执行成功：%v", err)
	}
	// custom 不做能力剔除：即使本机探测不到这些滤镜也要原样透传。
	if res.Filter != chain {
		t.Fatalf("自定义链应原样透传，实际：%q", res.Filter)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("自定义链不应有剔除项，实际：%v", res.Skipped)
	}
	if len(res.Applied) != 3 {
		t.Fatalf("Applied 应拆出 3 项，实际：%v", res.Applied)
	}

	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	if !strings.Contains(joined, "-af "+chain) {
		t.Fatalf("自定义链未透传给 -af：%s", joined)
	}
}

func TestEnhanceSelectFiltersByCapabilities(t *testing.T) {
	cases := []struct {
		name        string
		preset      string
		caps        Capabilities
		wantApplied []string
		wantSkipped []string
	}{
		{
			name:   "全部能力可用时保留整链",
			preset: PresetStrong,
			caps: Capabilities{
				SupportsDenoise: true, SupportsDeesser: true,
				SupportsLoudnorm: true, SupportsCompressor: true,
			},
			wantApplied: PresetFilters(PresetStrong),
		},
		{
			name:   "缺 afftdn 时剔除降噪并保留其余",
			preset: PresetStandard,
			caps: Capabilities{
				SupportsLoudnorm: true, SupportsCompressor: true,
			},
			wantApplied: []string{
				"highpass=f=80",
				"acompressor=threshold=-20dB:ratio=4:attack=5:release=50",
				"loudnorm=I=-16:TP=-1.5:LRA=11",
			},
			wantSkipped: []string{"afftdn=nf=-25:nr=10"},
		},
		{
			name:   "缺 deesser 时强力预设退化为无齿音抑制",
			preset: PresetStrong,
			caps: Capabilities{
				SupportsDenoise: true, SupportsLoudnorm: true, SupportsCompressor: true,
			},
			wantApplied: []string{
				"highpass=f=90",
				"afftdn=nf=-20:nr=15",
				"acompressor=threshold=-18dB:ratio=6:attack=5:release=80",
				"loudnorm=I=-16:TP=-1.5:LRA=11",
			},
			wantSkipped: []string{"deesser=i=0.4:m=0.5:f=0.5:s=o"},
		},
		{
			name:        "所有能力都不可用时只剩 highpass",
			preset:      PresetStandard,
			caps:        Capabilities{},
			wantApplied: []string{"highpass=f=80"},
			wantSkipped: []string{
				"afftdn=nf=-25:nr=10",
				"acompressor=threshold=-20dB:ratio=4:attack=5:release=50",
				"loudnorm=I=-16:TP=-1.5:LRA=11",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applied, skipped := selectFilters(PresetFilters(tc.preset), tc.caps)
			if strings.Join(applied, ",") != strings.Join(tc.wantApplied, ",") {
				t.Fatalf("Applied 不符：\n得到 %v\n期望 %v", applied, tc.wantApplied)
			}
			if strings.Join(skipped, ",") != strings.Join(tc.wantSkipped, ",") {
				t.Fatalf("Skipped 不符：\n得到 %v\n期望 %v", skipped, tc.wantSkipped)
			}
		})
	}
}

func TestFilterSupported(t *testing.T) {
	full := Capabilities{
		SupportsDenoise: true, SupportsDeesser: true,
		SupportsLoudnorm: true, SupportsCompressor: true,
	}
	// 能力齐全时所有滤镜都可用。
	for _, filter := range []string{
		"afftdn=nf=-25", "deesser=i=0.4", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"acompressor=threshold=-20dB:ratio=4", "highpass=f=80",
	} {
		if !filterSupported(filter, full) {
			t.Fatalf("能力齐全时 %q 应可用", filter)
		}
	}
	// 能力全缺时，只有未列入能力映射的滤镜（highpass 等）恒可用。
	empty := Capabilities{}
	if !filterSupported("highpass=f=90", empty) {
		t.Fatal("highpass 应恒可用，不受能力位影响")
	}
	if !filterSupported("volume=2dB", empty) {
		t.Fatal("未列入能力映射的自定义滤镜应视为可用")
	}
	for _, filter := range []string{
		"afftdn=nf=-25", "deesser=i=0.4",
		"loudnorm=I=-16:TP=-1.5:LRA=11", "acompressor=threshold=-20dB:ratio=4",
	} {
		if filterSupported(filter, empty) {
			t.Fatalf("能力缺失时 %q 应被判定不可用", filter)
		}
	}
}

func TestEnhanceArgsHelper(t *testing.T) {
	args := enhanceArgs("in.wav", "out.wav", "highpass=f=80,afftdn=nf=-25", defaultSampleRate, defaultChannels)
	want := []string{
		"-y", "-hide_banner", "-nostdin", "-i", "in.wav", "-vn",
		"-af", "highpass=f=80,afftdn=nf=-25",
		"-ac", "1", "-ar", "16000", "-acodec", "pcm_s16le", "out.wav",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("参数拼装不符：\n得到 %v\n期望 %v", args, want)
	}
}

func TestEnhanceRunsWithFilterChain(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	output := filepath.Join(dir, "nested", "out.wav")
	recordFile := filepath.Join(dir, "args.txt")
	ffmpegPath := fakeFFmpegEnhance(t, dir, recordFile, output)

	ff, err := New(ffmpegPath)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	// 假 ffmpeg 的 -filters 无输出，能力位全为 false：预设链会被剔除到只剩 highpass。
	res, err := ff.Enhance(context.Background(), EnhanceRequest{
		InputPath: input, OutputPath: output, Preset: PresetStandard,
		SampleRate: 44100, Channels: 2,
	})
	if err != nil {
		t.Fatalf("Enhance 不应失败：%v", err)
	}
	if res.Filter != "highpass=f=80" {
		t.Fatalf("应降级为仅 highpass，实际：%q", res.Filter)
	}
	if len(res.Skipped) != 3 {
		t.Fatalf("应剔除 3 个滤镜，实际：%v", res.Skipped)
	}
	if _, statErr := os.Stat(output); statErr != nil {
		t.Fatalf("输出文件未生成（父目录应自动创建）：%v", statErr)
	}

	raw, _ := os.ReadFile(recordFile)
	joined := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"-i " + input, "-vn", "-af highpass=f=80",
		"-ac 2", "-ar 44100", "-acodec pcm_s16le", output,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少参数 %q，实际：%s", want, joined)
		}
	}
	// 记录文件里同时有 -version/-filters 探测调用，逐行找出真正的降噪调用再校验前缀。
	if !hasEnhanceInvocation(raw) {
		t.Fatalf("未找到以 -y 开头的降噪调用，实际记录：%s", joined)
	}
}

// hasEnhanceInvocation 判断参数记录中是否存在以 -y 开头的 ffmpeg 转码调用。
func hasEnhanceInvocation(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-y ") {
			return true
		}
	}
	return false
}

func TestEnhanceDegradesWithoutDenoiseFilter(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	output := filepath.Join(dir, "out.wav")

	// 只支持 highpass 的假 ffmpeg：强力预设剔除后只剩 highpass，仍应执行而不是报能力错误。
	ff := newFFmpegWithFilters(t, dir, fakeFiltersOnlyHighpass)
	_, err := ff.Enhance(context.Background(), EnhanceRequest{
		InputPath: input, OutputPath: output, Preset: PresetStrong,
	})
	// 假 ffmpeg 不会真的产出文件，所以预期是「未生成有效输出文件」；
	// 关键是它走到了真正执行 FFmpeg 这一步，而不是因能力缺失报错。
	if err == nil {
		t.Fatal("假 ffmpeg 不产出文件时应返回错误")
	}
	if !strings.Contains(err.Error(), "未生成有效输出文件") {
		t.Fatalf("应因缺少输出文件而失败，而不是能力问题，实际：%v", err)
	}
}

func TestSelectFiltersEmptyChainStaysEmpty(t *testing.T) {
	// 链本身为空（或全是空串）时，剔除后仍为空，不 panic。
	for _, filters := range [][]string{nil, {}, {"", "  "}} {
		applied, skipped := selectFilters(filters, Capabilities{})
		if len(applied) != 0 || len(skipped) != 0 {
			t.Fatalf("空链应返回空结果，实际 applied=%v skipped=%v", applied, skipped)
		}
	}
}

func TestEnhanceRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	ff := newFFmpegWithFilters(t, dir, fakeFiltersWithAudio)
	output := filepath.Join(dir, "out.wav")

	if _, err := ff.Enhance(context.Background(), EnhanceRequest{
		Preset: PresetStandard, OutputPath: output,
	}); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空输入路径应报中文错误，实际：%v", err)
	}

	if _, err := ff.Enhance(context.Background(), EnhanceRequest{
		Preset: PresetStandard, InputPath: writeFile(t, dir, "i.wav", "x"),
	}); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空输出路径应报中文错误，实际：%v", err)
	}

	missing := filepath.Join(dir, "不存在.wav")
	if _, err := ff.Enhance(context.Background(), EnhanceRequest{
		Preset: PresetStandard, InputPath: missing, OutputPath: output,
	}); err == nil || !strings.Contains(err.Error(), "输入文件不存在") {
		t.Fatalf("输入不存在应报中文错误，实际：%v", err)
	}

	if _, err := ff.Enhance(context.Background(), EnhanceRequest{
		Preset: PresetStandard, InputPath: dir, OutputPath: output,
	}); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("目录输入应报错，实际：%v", err)
	}
}

func TestEnhanceErrorMessageIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	output := filepath.Join(dir, "out.wav")
	writeScript(t, dir, "ffmpeg",
		"echo [af#0] Error initializing filter 'afftdn': Invalid argument 1>&2\r\nexit /b 1\r\n")

	ff, err := New(filepath.Join(dir, "ffmpeg.bat"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	_, err = ff.Enhance(context.Background(), EnhanceRequest{
		InputPath: input, OutputPath: output,
		Preset: PresetCustom, FilterChain: "afftdn=nf=-25",
	})
	if err == nil {
		t.Fatal("ffmpeg 失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "音频降噪失败") {
		t.Fatalf("错误应为中文前缀，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "Invalid argument") {
		t.Fatalf("错误应包含 stderr 关键片段，实际：%v", err)
	}
}

func TestEnhanceFailsWhenNoOutputProduced(t *testing.T) {
	dir := t.TempDir()
	input := writeFile(t, dir, "input.wav", "fake")
	ff, err := New(writeScript(t, dir, "ffmpeg", "exit /b 0\r\n"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	_, err = ff.Enhance(context.Background(), EnhanceRequest{
		InputPath: input, OutputPath: filepath.Join(dir, "out.wav"),
		Preset: PresetCustom, FilterChain: "highpass=f=80",
	})
	if err == nil || !strings.Contains(err.Error(), "未生成有效输出文件") {
		t.Fatalf("未产出文件时应报错，实际：%v", err)
	}
}

func TestEnhanceNilReceiver(t *testing.T) {
	var ff *FFmpeg
	if _, err := ff.Enhance(context.Background(), EnhanceRequest{Preset: PresetStandard}); err == nil {
		t.Fatal("nil 接收者应返回错误")
	}
	if _, err := (Enhancer)(ff).Enhance(context.Background(), EnhanceRequest{Preset: PresetOff}); err == nil {
		t.Fatal("通过接口调用 nil 接收者也应返回错误")
	}
}
