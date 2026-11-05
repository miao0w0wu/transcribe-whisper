"""faster-whisper 识别工作进程（由 Go 侧以子进程方式调用）。

协议：stdin 读一行 JSON 入参，stdout 每行输出一个 JSON 事件。
    {"type":"progress","ratio":0.42,"message":"..."}
    {"type":"segment","id":"1","sequence":1,"start":0.0,"end":1.2,"text":"...","language":"en","confidence":0.9}
    {"type":"done","language":"en","language_score":0.98,"duration":123.4,"model":"...","engine":"faster-whisper"}
    {"type":"error","message":"中文错误"}

约定：
    * stdout 只写 JSON 行，其它日志一律写 stderr；
    * 出错时输出 error 事件并以非 0 退出码结束；
    * 所有面向用户的错误信息使用中文。
"""

import json
import os
import sys
import traceback

ENGINE = "faster-whisper"
MODEL_CACHE_DIR = "models"


def emit(event):
    """向 stdout 写一个 JSON 行并立即刷新。"""
    sys.stdout.write(json.dumps(event, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def log(message):
    """向 stderr 写日志，stdout 保持干净。"""
    sys.stderr.write(str(message) + "\n")
    sys.stderr.flush()


def fail(message):
    """输出 error 事件并以非 0 退出。"""
    emit({"type": "error", "message": str(message)})
    sys.exit(1)


def read_request():
    """读取入参：优先 stdin 一行 JSON，其次第一个命令行参数。"""
    raw = ""
    if len(sys.argv) > 1:
        raw = sys.argv[1]
    else:
        raw = sys.stdin.readline()
    raw = (raw or "").strip()
    if not raw:
        fail("未收到识别参数，请检查调用方式")
    try:
        data = json.loads(raw)
    except Exception as exc:  # noqa: BLE001
        fail("识别参数不是合法 JSON：%s" % exc)
    if not isinstance(data, dict):
        fail("识别参数格式错误：应为 JSON 对象")
    return data


def normalize_language(value):
    """把 Go 侧的 auto 等占位值转成 None，交给模型自动检测。"""
    text = (value or "").strip()
    if text == "" or text.lower() in ("auto", "detect"):
        return None
    return text


def normalize_choice(value, allowed, default):
    """校验枚举值，不合法时回落到默认值。"""
    text = (value or "").strip().lower()
    if text in allowed:
        return text
    return default


def normalize_type(value):
    """计算精度：auto 保持 None 让 ctranslate2 自行选择。"""
    text = (value or "").strip().lower()
    if text in ("", "auto"):
        return None
    return text


def positive_float(value, default):
    """取正浮点数；缺失、非法或非正时回落到默认值。"""
    try:
        number = float(value)
    except (TypeError, ValueError):
        return default
    if number <= 0:
        return default
    return number


def positive_int(value, default):
    """取正整数值；缺失、非法或非正时回落到默认值。"""
    try:
        number = int(value)
    except (TypeError, ValueError):
        return default
    if number <= 0:
        return default
    return number


def optional_bool(value, default):
    """取布尔值；None 表示调用方未设置，回落到默认值。"""
    if value is None:
        return default
    return bool(value)


def build_vad_parameters(request):
    """组装 faster-whisper 的 vad_parameters。

    Go 侧只传非零值，未设置的键直接不出现，由 faster-whisper 用自己的默认值。
    """
    params = {}
    threshold = positive_float(request.get("vad_threshold"), 0)
    if threshold > 0:
        params["threshold"] = max(0.01, min(0.99, threshold))
    min_silence = positive_int(request.get("vad_min_silence_ms"), 0)
    if min_silence > 0:
        params["min_silence_duration_ms"] = min_silence
    padding = positive_int(request.get("vad_padding_ms"), 0)
    if padding > 0:
        params["speech_pad_ms"] = padding
    return params or None


def resolve_model_path(request):
    """优先使用本地模型目录，其次带缓存目录使用模型名。"""
    model_path = (request.get("model_path") or "").strip()
    if model_path:
        if not os.path.isdir(model_path):
            fail("本地模型目录不存在：%s" % model_path)
        return model_path, None

    model_name = (request.get("model_name") or "").strip()
    if not model_name:
        fail("未指定模型：请先在设置中下载模型或填写模型名称")
    download_root = (request.get("download_root") or "").strip()
    if not download_root:
        base = os.path.dirname(os.path.abspath(__file__))
        download_root = os.path.join(base, MODEL_CACHE_DIR)
    try:
        os.makedirs(download_root, exist_ok=True)
    except Exception as exc:  # noqa: BLE001
        fail("无法创建模型缓存目录 %s：%s" % (download_root, exc))
    return model_name, download_root


def build_model(request):
    """创建 WhisperModel；model_path 优先，否则用 model_name 下载。"""
    try:
        from faster_whisper import WhisperModel
    except Exception as exc:  # noqa: BLE001
        fail("未安装 faster-whisper，请先执行 pip install faster-whisper（原因：%s）" % exc)

    model_ref, download_root = resolve_model_path(request)
    device = normalize_choice(request.get("device"), ("auto", "cpu", "cuda"), "auto")
    compute_type = normalize_type(request.get("compute_type"))

    kwargs = {"device": device}
    if compute_type:
        kwargs["compute_type"] = compute_type
    if download_root:
        kwargs["download_root"] = download_root

    # 允许通过 HF_ENDPOINT 指定镜像，例如 https://hf-mirror.com
    endpoint = os.environ.get("HF_ENDPOINT", "").strip()
    if endpoint:
        log("使用 HF_ENDPOINT=%s" % endpoint)

    emit({"type": "progress", "ratio": -1, "message": "正在加载模型 %s ..." % model_ref})
    try:
        model = WhisperModel(model_ref, **kwargs)
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("加载模型失败（%s）：%s" % (model_ref, exc))

    align_mel_channels(model, model_ref)
    return model


def align_mel_channels(model, model_ref):
    """让特征提取器的 mel 通道数对齐模型权重要求的通道数。

    背景：faster-whisper 用 FeatureExtractor 算 log-Mel 特征，mel 滤波器组
    数量取自模型目录里 preprocessor_config.json 的 feature_size；该文件缺失时
    FeatureExtractor 静默回落到硬编码默认值 80。而 large-v3 的权重是按 128 个
    mel 通道训练的，于是推理时报：

        Invalid input features shape: expected an input with shape
        (1, 128, 3000), but got an input with shape (1, 80, 3000) instead

    这个错误只在第一次推理时抛，而且信息完全不指向根因，用户很难自查。
    更麻烦的是，老版本应用下载的 large-v3 目录里根本没有这个配置文件，
    即使升级程序也不会自动补上。

    这里在加载模型后做一次自检：发现两边通道数不一致时，按模型要求的
    n_mels 重建 FeatureExtractor，并同步重算依赖它的三个派生量。这样
    无论目录里有没有 preprocessor_config.json，都能正常识别。
    """
    try:
        from faster_whisper.feature_extractor import FeatureExtractor
    except Exception:  # noqa: BLE001
        return

    extractor = getattr(model, "feature_extractor", None)
    inner = getattr(model, "model", None)
    if extractor is None or inner is None:
        return

    try:
        want = int(getattr(inner, "n_mels", 0) or 0)
        mel_filters = getattr(extractor, "mel_filters", None)
        have = int(mel_filters.shape[0]) if mel_filters is not None else 0
    except Exception:  # noqa: BLE001
        return

    if want <= 0 or have <= 0 or want == have:
        return

    log("特征维度与模型不一致（提取器 %d / 模型 %d），按模型要求重建特征提取器" % (have, want))
    try:
        feat_kwargs = dict(getattr(model, "feat_kwargs", {}) or {})
        feat_kwargs["feature_size"] = want
        model.feature_extractor = FeatureExtractor(**feat_kwargs)
        # 这三个量都是从 feature_extractor 派生的，必须一起重算，
        # 否则时间轴会按错误的 hop 换算，字幕整体错位。
        model.num_samples_per_token = model.feature_extractor.hop_length * model.input_stride
        model.frames_per_second = (
            model.feature_extractor.sampling_rate // model.feature_extractor.hop_length
        )
        model.tokens_per_second = (
            model.feature_extractor.sampling_rate // model.num_samples_per_token
        )
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail(
            "模型 %s 需要 %d 个 mel 通道，但特征提取器只有 %d 个，且自动修复失败：%s。"
            "请删除模型目录后重新下载（模型目录需要包含 preprocessor_config.json）"
            % (model_ref, want, have, exc)
        )
    else:
        log("已按模型要求重建特征提取器（%d 个 mel 通道）" % want)



def make_progress_callback(total_duration):
    """按音频时长把已识别秒数换算成 0..1 的比例。"""
    last_sent = {"ratio": -1.0}

    def callback(seconds):
        if total_duration and total_duration > 0:
            ratio = max(0.0, min(1.0, float(seconds) / float(total_duration)))
        else:
            ratio = -1.0
        if ratio >= 0 and abs(ratio - last_sent["ratio"]) < 0.005:
            return
        last_sent["ratio"] = ratio
        emit({
            "type": "progress",
            "ratio": round(ratio, 4),
            "message": "已识别 %.1f / %.1f 秒" % (seconds, total_duration or 0),
        })

    return callback


def media_duration(audio_path):
    """尽量读取音频时长，失败时返回 0（进度改为未定义）。"""
    try:
        from faster_whisper.audio import decode_audio
        data = decode_audio(audio_path)
        return float(len(data)) / 16000.0
    except Exception:  # noqa: BLE001
        return 0.0


def transcribe(request):
    """执行识别并按协议输出事件。"""
    audio_path = (request.get("audio_path") or "").strip()
    if not audio_path:
        fail("未指定音频文件路径")
    if not os.path.isfile(audio_path):
        fail("音频文件不存在：%s" % audio_path)

    model = build_model(request)
    language = normalize_language(request.get("language"))

    beam_size = request.get("beam_size") or 0
    try:
        beam_size = int(beam_size)
    except Exception:  # noqa: BLE001
        beam_size = 0
    if beam_size <= 0:
        beam_size = 5

    vad_filter = bool(request.get("vad_filter"))
    initial_prompt = (request.get("initial_prompt") or "").strip() or None

    # 以下参数用于提高识别准确性；None 表示调用方未设置，交给 faster-whisper 默认。
    vad_parameters = build_vad_parameters(request) if vad_filter else None
    condition_on_previous_text = optional_bool(
        request.get("condition_on_previous_text"), True
    )
    no_speech_threshold = positive_float(request.get("no_speech_threshold"), 0) or None
    compression_ratio_threshold = (
        positive_float(request.get("compression_ratio_threshold"), 0) or None
    )

    emit({"type": "progress", "ratio": 0, "message": "开始识别"})
    duration = media_duration(audio_path)
    on_progress = make_progress_callback(duration)

    kwargs = {
        "beam_size": beam_size,
        "vad_filter": vad_filter,
        "language": language,
        "initial_prompt": initial_prompt,
        "condition_on_previous_text": condition_on_previous_text,
    }
    if vad_parameters:
        kwargs["vad_parameters"] = vad_parameters
    if no_speech_threshold is not None:
        kwargs["no_speech_threshold"] = no_speech_threshold
    if compression_ratio_threshold is not None:
        kwargs["compression_ratio_threshold"] = compression_ratio_threshold

    try:
        segments, info = model.transcribe(audio_path, **kwargs)
        detected_language = getattr(info, "language", None) or (language or "")
        language_probability = float(getattr(info, "language_probability", 0.0) or 0.0)
        audio_duration = float(getattr(info, "duration", 0.0) or 0.0)
        if audio_duration <= 0:
            audio_duration = duration

        sequence = 0
        for segment in segments:
            sequence += 1
            text = (segment.text or "").strip()
            start = float(segment.start or 0.0)
            end = float(segment.end or 0.0)
            if end < start:
                end = start
            emit({
                "type": "segment",
                "id": str(getattr(segment, "id", sequence)),
                "sequence": sequence,
                "start": round(start, 3),
                "end": round(end, 3),
                "text": text,
                "language": detected_language,
                "confidence": round(float(getattr(segment, "avg_logprob", 0.0) or 0.0), 4),
            })
            on_progress(end)
    except KeyboardInterrupt:
        fail("识别已被取消")
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("识别失败：%s" % exc)

    emit({
        "type": "done",
        "language": detected_language,
        "language_score": round(language_probability, 4),
        "duration": round(audio_duration, 3),
        "model": (request.get("model_name") or request.get("model_path") or ""),
        "engine": ENGINE,
    })


def main():
    request = read_request()
    transcribe(request)
    return 0


if __name__ == "__main__":
    sys.exit(main())
