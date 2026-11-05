package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// DefaultSettings 返回应用默认设置。调用方可以在此基础上覆盖。
func DefaultSettings() SettingsRecord {
	return SettingsRecord{
		FFmpegPath:          "",
		PythonPath:          "",
		ModelDir:            "",
		Mirror:              "hf-mirror",
		HuggingFaceEndpoint: "",
		AsrEngine:           "faster-whisper",
		AsrDevice:           "auto",
		ComputeType:         "auto",
		DefaultModel:        "small",
		TranslationProvider: "ollama",
		TranslationEndpoint: "",
		TranslationModel:    "",
		TranslationAPIKey:   "",
		CloudPrivacyConfirm: false,
		TranslationStyle:    "subtitle",
		Glossary:            map[string]string{},
		LocalModels:         map[string]string{},
		// 降噪默认关闭，采样率 0 表示沿用 16000。
		NoiseReduction:           "off",
		NoiseReductionFilter:     "",
		NoiseReductionSampleRate: 0,
		// 识别准确性默认值：束宽 5、启用 VAD、标准幻觉过滤。
		BeamSize:                  5,
		VadFilter:                 true,
		VadThreshold:              0.5,
		VadMinSilenceMs:           500,
		VadPaddingMs:              400,
		HallucinationFilter:       "medium",
		ConditionOnPreviousText:   false,
		NoSpeechThreshold:         0.6,
		CompressionRatioThreshold: 2.4,
	}
}

// LoadSettings 读取设置；没有任何记录时写入默认值并返回。
func (s *Store) LoadSettings(ctx context.Context) (SettingsRecord, error) {
	rec, err := s.querySettings(ctx)
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return SettingsRecord{}, err
	}
	// 首次启动：落库默认值，保证后续 UPSERT 有确定基线。
	rec = DefaultSettings()
	if err := s.SaveSettings(ctx, rec); err != nil {
		return SettingsRecord{}, fmt.Errorf("初始化默认设置失败: %w", err)
	}
	return rec, nil
}

func (s *Store) querySettings(ctx context.Context) (SettingsRecord, error) {
	var (
		rec        SettingsRecord
		privacy    int
		glossary   string
		localModel string
		vadFilter  int
		condition  int
	)
	err := s.db.QueryRowContext(ctx, `SELECT ffmpeg_path, python_path, model_dir, mirror,
		hugging_face_endpoint, asr_engine, asr_device, compute_type, default_model,
		translation_provider, translation_endpoint, translation_model, translation_api_key,
		cloud_privacy_confirm, translation_style, glossary_json, local_models_json,
		hugging_face_token,
		noise_reduction, noise_reduction_filter, noise_reduction_sample_rate,
		beam_size, vad_filter, vad_threshold, vad_min_silence_ms, vad_padding_ms,
		hallucination_filter, condition_on_previous_text, no_speech_threshold,
		compression_ratio_threshold
		FROM settings WHERE id = 1`).Scan(
		&rec.FFmpegPath, &rec.PythonPath, &rec.ModelDir, &rec.Mirror,
		&rec.HuggingFaceEndpoint, &rec.AsrEngine, &rec.AsrDevice, &rec.ComputeType, &rec.DefaultModel,
		&rec.TranslationProvider, &rec.TranslationEndpoint, &rec.TranslationModel, &rec.TranslationAPIKey,
		&privacy, &rec.TranslationStyle, &glossary, &localModel, &rec.HuggingFaceToken,
		&rec.NoiseReduction, &rec.NoiseReductionFilter, &rec.NoiseReductionSampleRate,
		&rec.BeamSize, &vadFilter, &rec.VadThreshold, &rec.VadMinSilenceMs, &rec.VadPaddingMs,
		&rec.HallucinationFilter, &condition, &rec.NoSpeechThreshold,
		&rec.CompressionRatioThreshold,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SettingsRecord{}, fmt.Errorf("读取设置: %w", ErrNotFound)
	}
	if err != nil {
		return SettingsRecord{}, fmt.Errorf("读取设置失败: %w", err)
	}
	rec.CloudPrivacyConfirm = privacy != 0
	// 布尔列以 0/1 存储，读回时还原成 bool。
	rec.VadFilter = vadFilter != 0
	rec.ConditionOnPreviousText = condition != 0
	rec.Glossary = map[string]string{}
	if strings.TrimSpace(glossary) != "" {
		// 术语表损坏不应导致整条设置读不出来，按空术语表处理。
		if unmarshalErr := json.Unmarshal([]byte(glossary), &rec.Glossary); unmarshalErr != nil {
			rec.Glossary = map[string]string{}
		}
	}
	rec.LocalModels = map[string]string{}
	if strings.TrimSpace(localModel) != "" {
		// 同上：本地模型记录损坏时按空处理，不影响其他设置。
		if unmarshalErr := json.Unmarshal([]byte(localModel), &rec.LocalModels); unmarshalErr != nil {
			rec.LocalModels = map[string]string{}
		}
	}
	return rec, nil
}

// SaveSettings 以单行 UPSERT 方式保存设置（id 固定为 1）。
func (s *Store) SaveSettings(ctx context.Context, rec SettingsRecord) error {
	privacy := 0
	if rec.CloudPrivacyConfirm {
		privacy = 1
	}
	glossaryBytes, err := json.Marshal(rec.Glossary)
	if err != nil || rec.Glossary == nil {
		glossaryBytes = []byte("{}")
	}
	localBytes, err := json.Marshal(rec.LocalModels)
	if err != nil || rec.LocalModels == nil {
		localBytes = []byte("{}")
	}
	style := strings.TrimSpace(rec.TranslationStyle)
	if style == "" {
		style = "subtitle"
	}

	// 数值项兜底：只拦截越界的脏数据，避免 Python 侧拿到非法参数直接报错。
	// 零值表示"未设置"，原样保留，由上层回落默认值（不在这里强行改写）。
	beamSize := rec.BeamSize
	if beamSize < 0 || beamSize > 10 {
		beamSize = 5
	}
	vadThreshold := rec.VadThreshold
	if vadThreshold < 0 || vadThreshold > 1 {
		vadThreshold = 0.5
	}
	vadMinSilenceMs := rec.VadMinSilenceMs
	if vadMinSilenceMs < 0 || vadMinSilenceMs > 2000 {
		vadMinSilenceMs = 500
	}
	vadPaddingMs := rec.VadPaddingMs
	if vadPaddingMs < 0 || vadPaddingMs > 2000 {
		vadPaddingMs = 400
	}
	noSpeechThreshold := rec.NoSpeechThreshold
	if noSpeechThreshold < 0 || noSpeechThreshold > 1 {
		noSpeechThreshold = 0.6
	}
	compressionRatioThreshold := rec.CompressionRatioThreshold
	if compressionRatioThreshold < 0 || compressionRatioThreshold > 5 {
		compressionRatioThreshold = 2.4
	}
	sampleRate := rec.NoiseReductionSampleRate
	if sampleRate < 0 {
		sampleRate = 0
	}
	// 布尔列以 0/1 存储，与 cloud_privacy_confirm 保持一致。
	vadFilter := 0
	if rec.VadFilter {
		vadFilter = 1
	}
	conditionOnPreviousText := 0
	if rec.ConditionOnPreviousText {
		conditionOnPreviousText = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (
		id, ffmpeg_path, python_path, model_dir, mirror, hugging_face_endpoint, asr_engine,
		asr_device, compute_type, default_model, translation_provider, translation_endpoint,
		translation_model, translation_api_key, cloud_privacy_confirm, translation_style,
		glossary_json, local_models_json, hugging_face_token,
		noise_reduction, noise_reduction_filter, noise_reduction_sample_rate,
		beam_size, vad_filter, vad_threshold, vad_min_silence_ms, vad_padding_ms,
		hallucination_filter, condition_on_previous_text, no_speech_threshold,
		compression_ratio_threshold
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		ffmpeg_path = excluded.ffmpeg_path,
		python_path = excluded.python_path,
		model_dir = excluded.model_dir,
		mirror = excluded.mirror,
		hugging_face_endpoint = excluded.hugging_face_endpoint,
		asr_engine = excluded.asr_engine,
		asr_device = excluded.asr_device,
		compute_type = excluded.compute_type,
		default_model = excluded.default_model,
		translation_provider = excluded.translation_provider,
		translation_endpoint = excluded.translation_endpoint,
		translation_model = excluded.translation_model,
		translation_api_key = excluded.translation_api_key,
		cloud_privacy_confirm = excluded.cloud_privacy_confirm,
		translation_style = excluded.translation_style,
		glossary_json = excluded.glossary_json,
		local_models_json = excluded.local_models_json,
		hugging_face_token = excluded.hugging_face_token,
		noise_reduction = excluded.noise_reduction,
		noise_reduction_filter = excluded.noise_reduction_filter,
		noise_reduction_sample_rate = excluded.noise_reduction_sample_rate,
		beam_size = excluded.beam_size,
		vad_filter = excluded.vad_filter,
		vad_threshold = excluded.vad_threshold,
		vad_min_silence_ms = excluded.vad_min_silence_ms,
		vad_padding_ms = excluded.vad_padding_ms,
		hallucination_filter = excluded.hallucination_filter,
		condition_on_previous_text = excluded.condition_on_previous_text,
		no_speech_threshold = excluded.no_speech_threshold,
		compression_ratio_threshold = excluded.compression_ratio_threshold`,
		rec.FFmpegPath, rec.PythonPath, rec.ModelDir, rec.Mirror, rec.HuggingFaceEndpoint,
		rec.AsrEngine, rec.AsrDevice, rec.ComputeType, rec.DefaultModel, rec.TranslationProvider,
		rec.TranslationEndpoint, rec.TranslationModel, rec.TranslationAPIKey, privacy,
		style, string(glossaryBytes), string(localBytes), rec.HuggingFaceToken,
		rec.NoiseReduction, rec.NoiseReductionFilter, sampleRate,
		beamSize, vadFilter, vadThreshold, vadMinSilenceMs, vadPaddingMs,
		rec.HallucinationFilter, conditionOnPreviousText, noSpeechThreshold,
		compressionRatioThreshold,
	)
	if err != nil {
		return fmt.Errorf("保存设置失败: %w", err)
	}
	return nil
}
