# transcribe-whisper v0.1.0

发布日期：2026-11-05

## 版本概览

首个可用版本，提供从音视频导入、语音识别、翻译、字幕校对到字幕导出和视频压制的完整本地工作流。

## 主要功能

- 支持 faster-whisper 与 transformers 双识别引擎，按模型格式自动路由。
- 支持 tiny、base、small、medium、large-v3 及 cohere-transcribe-03-2026 等模型。
- 支持模型下载、断点续传、取消、删除保护，以及导入本地模型目录。
- 支持 FFmpeg 手动选择或下载完整版，并支持字幕压制到 MP4。
- 支持 Ollama 与 OpenAI-compatible 翻译服务，包含批处理、重试、缓存和术语表。
- 支持任务队列、暂停、恢复、取消、重试和异常退出恢复。
- 支持工作台字幕搜索、筛选、行内编辑和翻译重试。
- 支持 SRT、VTT、TXT、JSON 和双语 SRT 导出。
- 支持 Windows 绿色版目录，任务库、模型和 FFmpeg 可随数据目录迁移。

## 验证情况

- `go test ./...` 通过。
- 前端 `npm run build` 通过。
- `wails build` 打包通过。
- 已验证本地识别、Ollama 翻译、五种格式导出和字幕压制流程。
- 已验证 cohere-transcribe-03-2026 的 transformers 识别流程。

## 已知限制

- FunASR 后端已实现，但尚未在模型管理和界面中完整暴露。
- 长音频断点续跑尚未完成。
- 术语表暂不支持导入和导出。
- 字幕压制的字体、位置和描边参数仍较有限。
- CI 与 Windows 安装包验证仍待补充。
