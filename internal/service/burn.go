package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"transcribe-whisper/internal/exporter"
	"transcribe-whisper/internal/media"
)

// BurnOptions 是字幕压制的外观参数，零值表示交给 FFmpeg / libass 用默认值。
type BurnOptions struct {
	FontName string
	FontSize int
	MarginV  int
}

// BurnSubtitles 把任务字幕烧进源视频，返回压制产物的绝对路径。
//
// 压制用与导出相同的文本策略：优先译文，译文为空时回退原文，
// 保证画面里看到的字幕和导出的字幕文件一致。
func (q *Queue) BurnSubtitles(ctx context.Context, taskID, outputPath string, opts BurnOptions) (string, error) {
	if q.deps.Burner == nil {
		return "", errors.New("当前环境不支持字幕压制，请先在设置中指定带 libass 的 FFmpeg")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", errors.New("字幕压制失败：任务 id 不能为空")
	}
	// 任务执行中字幕还在变，此时压制会得到半成品画面。
	if q.busy(ctx, taskID) {
		return "", errors.New("任务正在执行中，请等任务完成后再压制字幕")
	}

	rec, err := q.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	source := strings.TrimSpace(rec.SourceFile)
	if source == "" {
		return "", errors.New("字幕压制失败：任务没有记录源视频路径")
	}
	if stat, statErr := os.Stat(source); statErr != nil {
		return "", fmt.Errorf("字幕压制失败：源视频不存在 %s", source)
	} else if stat.IsDir() {
		return "", fmt.Errorf("字幕压制失败：源视频路径是目录 %s", source)
	}

	segments, err := q.deps.Store.ListSegments(ctx, taskID)
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return "", errors.New("任务还没有字幕，暂时无法压制")
	}

	content, err := exporter.Export(toExporterSegments(segments), exporter.Options{
		Format:        exporter.FormatSRT,
		IncludeSource: true,
		UseSource:     true,
	})
	if err != nil {
		return "", fmt.Errorf("生成压制用字幕失败：%v", err)
	}

	output := strings.TrimSpace(outputPath)
	if output == "" {
		return "", errors.New("字幕压制失败：输出路径不能为空")
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		outputAbs = output
	}
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		sourceAbs = source
	}
	// 就地覆盖会直接毁掉源视频，必须拒绝。
	if strings.EqualFold(outputAbs, sourceAbs) {
		return "", errors.New("字幕压制失败：输出路径不能与源视频相同，请另选文件名")
	}

	dir := q.workDir(taskID)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建任务工作目录失败：%v", err)
	}
	subtitlePath := filepath.Join(dir, "burn.srt")
	if err := os.WriteFile(subtitlePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入压制用字幕文件失败：%v", err)
	}

	if err := q.deps.Burner.Burn(ctx, media.BurnRequest{
		VideoPath:    sourceAbs,
		SubtitlePath: subtitlePath,
		OutputPath:   outputAbs,
		FontName:     strings.TrimSpace(opts.FontName),
		FontSize:     opts.FontSize,
		MarginV:      opts.MarginV,
	}); err != nil {
		return "", err
	}
	return outputAbs, nil
}
