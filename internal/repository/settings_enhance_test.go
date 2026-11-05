package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestEnhanceSettingsDefaults 验证新库首次读取时的降噪与识别准确性默认值。
func TestEnhanceSettingsDefaults(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}

	// 降噪：默认关闭，无自定义滤镜链，采样率沿用 16000。
	if got.NoiseReduction != "off" {
		t.Fatalf("NoiseReduction 默认应为 off，实际 %q", got.NoiseReduction)
	}
	if got.NoiseReductionFilter != "" {
		t.Fatalf("NoiseReductionFilter 默认应为空，实际 %q", got.NoiseReductionFilter)
	}
	if got.NoiseReductionSampleRate != 0 {
		t.Fatalf("NoiseReductionSampleRate 默认应为 0，实际 %d", got.NoiseReductionSampleRate)
	}

	// 识别准确性默认值。
	if got.BeamSize != 5 {
		t.Fatalf("BeamSize 默认应为 5，实际 %d", got.BeamSize)
	}
	if !got.VadFilter {
		t.Fatal("VadFilter 默认应为 true")
	}
	if got.VadThreshold != 0.5 {
		t.Fatalf("VadThreshold 默认应为 0.5，实际 %v", got.VadThreshold)
	}
	if got.VadMinSilenceMs != 500 {
		t.Fatalf("VadMinSilenceMs 默认应为 500，实际 %d", got.VadMinSilenceMs)
	}
	if got.VadPaddingMs != 400 {
		t.Fatalf("VadPaddingMs 默认应为 400，实际 %d", got.VadPaddingMs)
	}
	if got.HallucinationFilter != "medium" {
		t.Fatalf("HallucinationFilter 默认应为 medium，实际 %q", got.HallucinationFilter)
	}
	if got.ConditionOnPreviousText {
		t.Fatal("ConditionOnPreviousText 默认应为 false")
	}
	if got.NoSpeechThreshold != 0.6 {
		t.Fatalf("NoSpeechThreshold 默认应为 0.6，实际 %v", got.NoSpeechThreshold)
	}
	if got.CompressionRatioThreshold != 2.4 {
		t.Fatalf("CompressionRatioThreshold 默认应为 2.4，实际 %v", got.CompressionRatioThreshold)
	}

	// 默认值应真的落库，而不是只在内存里拼出来。
	var (
		noise    string
		beam     int
		vad      int
		hallucin string
	)
	if err := store.DB().QueryRow(
		`SELECT noise_reduction, beam_size, vad_filter, hallucination_filter FROM settings WHERE id = 1`,
	).Scan(&noise, &beam, &vad, &hallucin); err != nil {
		t.Fatalf("查询新增列默认值失败: %v", err)
	}
	if noise != "off" || beam != 5 || vad != 1 || hallucin != "medium" {
		t.Fatalf("落库默认值不符: noise=%q beam=%d vad=%d hallucination=%q", noise, beam, vad, hallucin)
	}
}

// TestEnhanceSettingsRoundTrip 验证新字段保存后能原样读回（含 bool 与浮点）。
func TestEnhanceSettingsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	want := DefaultSettings()
	want.NoiseReduction = "custom"
	want.NoiseReductionFilter = "highpass=f=80,afftdn=nf=-25"
	want.NoiseReductionSampleRate = 44100
	want.BeamSize = 8
	want.VadFilter = false
	want.VadThreshold = 0.35
	want.VadMinSilenceMs = 1200
	want.VadPaddingMs = 200
	want.HallucinationFilter = "high"
	want.ConditionOnPreviousText = true
	want.NoSpeechThreshold = 0.75
	want.CompressionRatioThreshold = 3.1

	if err := store.SaveSettings(ctx, want); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.NoiseReduction != want.NoiseReduction ||
		got.NoiseReductionFilter != want.NoiseReductionFilter ||
		got.NoiseReductionSampleRate != want.NoiseReductionSampleRate {
		t.Fatalf("降噪字段往返不一致\n got=%+v\nwant=%+v", got, want)
	}
	if got.BeamSize != want.BeamSize ||
		got.VadFilter != want.VadFilter ||
		got.VadThreshold != want.VadThreshold ||
		got.VadMinSilenceMs != want.VadMinSilenceMs ||
		got.VadPaddingMs != want.VadPaddingMs ||
		got.HallucinationFilter != want.HallucinationFilter ||
		got.ConditionOnPreviousText != want.ConditionOnPreviousText ||
		got.NoSpeechThreshold != want.NoSpeechThreshold ||
		got.CompressionRatioThreshold != want.CompressionRatioThreshold {
		t.Fatalf("识别准确性字段往返不一致\n got=%+v\nwant=%+v", got, want)
	}

	// 再保存一次走 UPDATE 分支，仍然单行且值正确。
	want.VadFilter = true
	want.ConditionOnPreviousText = false
	want.BeamSize = 1
	if err := store.SaveSettings(ctx, want); err != nil {
		t.Fatalf("再次保存设置失败: %v", err)
	}
	got, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !got.VadFilter || got.ConditionOnPreviousText || got.BeamSize != 1 {
		t.Fatalf("二次保存后字段不符: %+v", got)
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(1) FROM settings").Scan(&count); err != nil {
		t.Fatalf("查询设置行数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("UPSERT 应保持单行，实际 %d 行", count)
	}

	// bool 必须以 0/1 存储，供前端与 Python 侧直接读取。
	var (
		vadStored  int
		condStored int
	)
	if err := store.DB().QueryRow(
		`SELECT vad_filter, condition_on_previous_text FROM settings WHERE id = 1`,
	).Scan(&vadStored, &condStored); err != nil {
		t.Fatalf("查询布尔列失败: %v", err)
	}
	if vadStored != 1 || condStored != 0 {
		t.Fatalf("布尔列应以 0/1 存储，实际 vad=%d condition=%d", vadStored, condStored)
	}
}

// TestEnhanceSettingsClamp 验证越界脏数据被兜底成合法值。
// 注意：零值表示"未设置"，不做改写，否则会破坏设置的零值往返语义。
func TestEnhanceSettingsClamp(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	dirty := DefaultSettings()
	dirty.BeamSize = 99
	dirty.VadThreshold = 7.5
	dirty.VadMinSilenceMs = -20
	dirty.VadPaddingMs = 99999
	dirty.NoSpeechThreshold = -1
	dirty.CompressionRatioThreshold = 42
	dirty.NoiseReductionSampleRate = -16000

	if err := store.SaveSettings(ctx, dirty); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if got.BeamSize != 5 {
		t.Fatalf("越界 BeamSize 应兜底为 5，实际 %d", got.BeamSize)
	}
	if got.VadThreshold != 0.5 {
		t.Fatalf("越界 VadThreshold 应兜底为 0.5，实际 %v", got.VadThreshold)
	}
	if got.VadMinSilenceMs != 500 {
		t.Fatalf("越界 VadMinSilenceMs 应兜底为 500，实际 %d", got.VadMinSilenceMs)
	}
	if got.VadPaddingMs != 400 {
		t.Fatalf("越界 VadPaddingMs 应兜底为 400，实际 %d", got.VadPaddingMs)
	}
	if got.NoSpeechThreshold != 0.6 {
		t.Fatalf("越界 NoSpeechThreshold 应兜底为 0.6，实际 %v", got.NoSpeechThreshold)
	}
	if got.CompressionRatioThreshold != 2.4 {
		t.Fatalf("越界 CompressionRatioThreshold 应兜底为 2.4，实际 %v", got.CompressionRatioThreshold)
	}
	if got.NoiseReductionSampleRate != 0 {
		t.Fatalf("负数采样率应兜底为 0，实际 %d", got.NoiseReductionSampleRate)
	}
}

// TestEnhanceSettingsLegacyTableMigration 验证只有旧列的 settings 表能被补齐新列。
func TestEnhanceSettingsLegacyTableMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-settings.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	// 早期版本的 settings 表：既没有术语表/风格等列，也没有本次新增的降噪与准确性列。
	if _, err := raw.Exec(`CREATE TABLE settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		ffmpeg_path TEXT NOT NULL DEFAULT '',
		python_path TEXT NOT NULL DEFAULT '',
		model_dir TEXT NOT NULL DEFAULT '',
		mirror TEXT NOT NULL DEFAULT 'hf-mirror',
		hugging_face_endpoint TEXT NOT NULL DEFAULT '',
		asr_engine TEXT NOT NULL DEFAULT 'faster-whisper',
		asr_device TEXT NOT NULL DEFAULT 'auto',
		compute_type TEXT NOT NULL DEFAULT 'auto',
		default_model TEXT NOT NULL DEFAULT 'small',
		translation_provider TEXT NOT NULL DEFAULT 'ollama',
		translation_endpoint TEXT NOT NULL DEFAULT '',
		translation_model TEXT NOT NULL DEFAULT '',
		translation_api_key TEXT NOT NULL DEFAULT '',
		cloud_privacy_confirm INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("创建旧 settings 表失败: %v", err)
	}
	// 旧库里已经有一行用户设置，迁移后不能被默认值覆盖。
	if _, err := raw.Exec(`INSERT INTO settings (id, default_model, cloud_privacy_confirm)
		VALUES (1, 'large-v3', 1)`); err != nil {
		t.Fatalf("写入旧设置行失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开旧库并迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// 新列必须真的被 ALTER TABLE 补齐。
	existing, err := store.tableColumns("settings")
	if err != nil {
		t.Fatalf("查询 settings 列失败: %v", err)
	}
	for _, name := range []string{
		"noise_reduction", "noise_reduction_filter", "noise_reduction_sample_rate",
		"beam_size", "vad_filter", "vad_threshold", "vad_min_silence_ms", "vad_padding_ms",
		"hallucination_filter", "condition_on_previous_text", "no_speech_threshold",
		"compression_ratio_threshold",
	} {
		if !existing[name] {
			t.Fatalf("迁移后缺少列 %s", name)
		}
	}

	// 旧数据保留，新列取 ADD COLUMN 的默认值。
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("迁移后读取设置失败: %v", err)
	}
	if got.DefaultModel != "large-v3" || !got.CloudPrivacyConfirm {
		t.Fatalf("旧设置被覆盖: %+v", got)
	}
	if got.NoiseReduction != "off" || got.NoiseReductionFilter != "" || got.NoiseReductionSampleRate != 0 {
		t.Fatalf("迁移后降噪默认值不符: %+v", got)
	}
	if got.BeamSize != 5 || !got.VadFilter || got.VadThreshold != 0.5 ||
		got.VadMinSilenceMs != 500 || got.VadPaddingMs != 400 ||
		got.HallucinationFilter != "medium" || got.ConditionOnPreviousText ||
		got.NoSpeechThreshold != 0.6 || got.CompressionRatioThreshold != 2.4 {
		t.Fatalf("迁移后识别准确性默认值不符: %+v", got)
	}

	// 迁移后的库要能正常写入新列。
	got.NoiseReduction = "strong"
	got.BeamSize = 9
	got.ConditionOnPreviousText = true
	if err := store.SaveSettings(ctx, got); err != nil {
		t.Fatalf("迁移后保存设置失败: %v", err)
	}
	again, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("迁移后再次读取设置失败: %v", err)
	}
	if again.NoiseReduction != "strong" || again.BeamSize != 9 || !again.ConditionOnPreviousText {
		t.Fatalf("迁移后写入新列失败: %+v", again)
	}

	// 幂等：重复打开同一库不应报重复列错误。
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开数据库失败: %v", err)
	}
	_ = store2.Close()
}
