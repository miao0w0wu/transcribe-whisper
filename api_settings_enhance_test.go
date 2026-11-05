package main

import (
	"testing"
)

// TestSettingsEnhanceFieldsRoundTrip 验证新增的降噪与识别准确性字段
// 能通过 UpdateSettings / GetSettings 完整往返，并落库到数据库。
func TestSettingsEnhanceFieldsRoundTrip(t *testing.T) {
	app := NewApp()
	t.Cleanup(func() { app.shutdown(nil) })

	// 先确认默认值符合契约。
	defaults := app.GetSettings()
	if defaults.NoiseReduction != "off" {
		t.Errorf("降噪默认应为 off，实际 %q", defaults.NoiseReduction)
	}
	if defaults.HallucinationFilter != "medium" {
		t.Errorf("幻觉过滤默认应为 medium，实际 %q", defaults.HallucinationFilter)
	}
	if defaults.BeamSize != 5 {
		t.Errorf("BeamSize 默认应为 5，实际 %d", defaults.BeamSize)
	}
	if !defaults.VadFilter {
		t.Error("VadFilter 默认应为 true")
	}
	if defaults.VadThreshold != 0.5 {
		t.Errorf("VadThreshold 默认应为 0.5，实际 %v", defaults.VadThreshold)
	}
	if defaults.VadMinSilenceMs != 500 {
		t.Errorf("VadMinSilenceMs 默认应为 500，实际 %d", defaults.VadMinSilenceMs)
	}
	if defaults.VadPaddingMs != 400 {
		t.Errorf("VadPaddingMs 默认应为 400，实际 %d", defaults.VadPaddingMs)
	}
	if defaults.NoSpeechThreshold != 0.6 {
		t.Errorf("NoSpeechThreshold 默认应为 0.6，实际 %v", defaults.NoSpeechThreshold)
	}
	if defaults.CompressionRatioThreshold != 2.4 {
		t.Errorf("CompressionRatioThreshold 默认应为 2.4，实际 %v", defaults.CompressionRatioThreshold)
	}

	// 写入一组非默认值。
	preset := "strong"
	filter := "anlmdn=s=7"
	sampleRate := 48000
	beam := 8
	vad := false
	vadThreshold := 0.35
	minSilence := 700
	padding := 250
	hallucination := "high"
	conditionOnPrevious := true
	noSpeech := 0.75
	compression := 3.1

	updated, err := app.UpdateSettings(SettingsPatch{
		NoiseReduction:            &preset,
		NoiseReductionFilter:      &filter,
		NoiseReductionSampleRate:  &sampleRate,
		BeamSize:                  &beam,
		VadFilter:                 &vad,
		VadThreshold:              &vadThreshold,
		VadMinSilenceMs:           &minSilence,
		VadPaddingMs:              &padding,
		HallucinationFilter:       &hallucination,
		ConditionOnPreviousText:   &conditionOnPrevious,
		NoSpeechThreshold:         &noSpeech,
		CompressionRatioThreshold: &compression,
	})
	if err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	check := func(name string, got, want interface{}) {
		t.Helper()
		if got != want {
			t.Errorf("%s 期望 %v，实际 %v", name, want, got)
		}
	}
	check("NoiseReduction", updated.NoiseReduction, "strong")
	check("NoiseReductionFilter", updated.NoiseReductionFilter, "anlmdn=s=7")
	check("NoiseReductionSampleRate", updated.NoiseReductionSampleRate, 48000)
	check("BeamSize", updated.BeamSize, 8)
	check("VadFilter", updated.VadFilter, false)
	check("VadThreshold", updated.VadThreshold, 0.35)
	check("VadMinSilenceMs", updated.VadMinSilenceMs, 700)
	check("VadPaddingMs", updated.VadPaddingMs, 250)
	check("HallucinationFilter", updated.HallucinationFilter, "high")
	check("ConditionOnPreviousText", updated.ConditionOnPreviousText, true)
	check("NoSpeechThreshold", updated.NoSpeechThreshold, 0.75)
	check("CompressionRatioThreshold", updated.CompressionRatioThreshold, 3.1)

	// 重新读取（走数据库），确认持久化生效。
	reloaded := app.GetSettings()
	check("重新读取 NoiseReduction", reloaded.NoiseReduction, "strong")
	check("重新读取 HallucinationFilter", reloaded.HallucinationFilter, "high")
	check("重新读取 BeamSize", reloaded.BeamSize, 8)
	check("重新读取 VadThreshold", reloaded.VadThreshold, 0.35)
	check("重新读取 ConditionOnPreviousText", reloaded.ConditionOnPreviousText, true)
}

// TestSettingsEnhanceFieldsValidation 验证非法取值会被规范化，不会写入脏数据。
func TestSettingsEnhanceFieldsValidation(t *testing.T) {
	app := NewApp()
	t.Cleanup(func() { app.shutdown(nil) })

	badPreset := "乱写的预设"
	badHallucination := "乱写的等级"
	badSampleRate := 12345
	tooBigBeam := 99
	tooBigThreshold := 5.5

	updated, err := app.UpdateSettings(SettingsPatch{
		NoiseReduction:           &badPreset,
		HallucinationFilter:      &badHallucination,
		NoiseReductionSampleRate: &badSampleRate,
		BeamSize:                 &tooBigBeam,
		VadThreshold:             &tooBigThreshold,
	})
	if err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	if updated.NoiseReduction != "off" {
		t.Errorf("非法降噪预设应回落 off，实际 %q", updated.NoiseReduction)
	}
	if updated.HallucinationFilter != "medium" {
		t.Errorf("非法幻觉等级应回落 medium，实际 %q", updated.HallucinationFilter)
	}
	if updated.NoiseReductionSampleRate != 0 {
		t.Errorf("非法采样率应回落 0，实际 %d", updated.NoiseReductionSampleRate)
	}
	if updated.BeamSize != 10 {
		t.Errorf("超上限 BeamSize 应夹到 10，实际 %d", updated.BeamSize)
	}
	if updated.VadThreshold != 1 {
		t.Errorf("超上限 VadThreshold 应夹到 1，实际 %v", updated.VadThreshold)
	}
}
