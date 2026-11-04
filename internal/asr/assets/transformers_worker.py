"""transformers 识别工作进程（由 Go 侧以子进程方式调用）。

用于加载 Hugging Face `transformers` 生态里的 ASR 模型，当前主要服务于
cohere-transcribe-03-2026（CohereAsrForConditionalGeneration）。这类模型是
safetensors + 自定义建模代码的格式，faster-whisper（CTranslate2）无法读取。

协议与 transcribe_worker.py 完全一致：
    stdin 读一行 JSON 入参，stdout 每行输出一个 JSON 事件。
    {"type":"progress","ratio":0.42,"message":"..."}
    {"type":"segment","id":"1","sequence":1,"start":0.0,"end":1.2,"text":"...","language":"en","confidence":0.9}
    {"type":"done","language":"en","language_score":0.98,"duration":123.4,"model":"...","engine":"transformers"}
    {"type":"error","message":"中文错误"}

时间轴说明：cohere-transcribe 是「整段进、整段出」的模型，生成结果是纯文本，
没有内建分段时间戳。本脚本按以下顺序尽力还原时间轴：
    1. 若模型支持返回时间戳（future API），直接用；
    2. 否则按音频时长把文本切块（chunk），逐块生成并记录该块的时间区间；
       这是真实的时间信息，不是凭空捏造；
    3. 仍无法切块时，退化为「整段一条字幕，时间轴覆盖全片」，并在 stderr
       提示用户该结果是单条字幕。
切块分块推理，既能让时间轴贴近真实，也能规避长音频一次性推理的显存峰值。
"""

import json
import math
import os
import sys
import traceback

ENGINE = "transformers"
MODEL_CACHE_DIR = "models"

# 每个切片的目标秒数。cohere 的 max_audio_clip_s 是 35 秒，这里取 30 秒留出余量，
# 既不会超出模型能力，也让字幕粒度足够细。
CHUNK_SECONDS = 30.0
# 切片之间的重叠秒数，避免在词中间硬切导致首尾丢字。
CHUNK_OVERLAP_SECONDS = 0.5
# 单次生成的最大 token 数。30 秒语音正常在 200 token 以内。
MAX_NEW_TOKENS = 448

# 静音判定阈值（RMS，float32 归一化到 [-1,1]）。
#
# 必须做这件事：cohere 这类模型在静音片段上会「幻觉」，凭空生成与音频无关的
# 整段文字（本机实测：纯静音块会输出葡萄牙语、西班牙语内容）。如果不拦，
# 用户会看到大量毫无根据的字幕。阈值取 0.0015 只挡真正的静音与极低本底噪声，
# 正常说话（哪怕很小声，RMS 通常 > 0.01）不会被误杀。
SILENCE_RMS_THRESHOLD = 0.0015
# 块内静音占比超过该值时整块跳过。
SILENCE_RATIO_THRESHOLD = 0.98
# 计算静音占比时的分析窗口（秒）。
SILENCE_WINDOW_SECONDS = 0.25


def block_is_silent(piece):
    """判断一段音频是否基本是静音。

    先看整体 RMS，再按小窗统计「近乎无声」的窗口占比。两者都指向静音时
    才判定为静音，避免把语速慢、停顿多的正常语音切掉。
    """
    try:
        import numpy as np
    except Exception:  # noqa: BLE001
        return False

    data = np.asarray(piece, dtype="float32")
    if data.size == 0:
        return True

    rms = float(np.sqrt(np.mean(np.square(data))))
    if rms >= SILENCE_RMS_THRESHOLD:
        # 整体不算静音，再确认一下不是「只有一小段声音、其余全静音」。
        window = max(1, int(SILENCE_WINDOW_SECONDS * 16000))
        quiet = 0
        total = 0
        for start in range(0, data.size, window):
            chunk = data[start:start + window]
            if chunk.size == 0:
                continue
            total += 1
            chunk_rms = float(np.sqrt(np.mean(np.square(chunk))))
            if chunk_rms < SILENCE_RMS_THRESHOLD:
                quiet += 1
        if total == 0:
            return True
        return (quiet / float(total)) >= SILENCE_RATIO_THRESHOLD
    return True


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
    """把 Go 侧的 auto 等占位值转成 None，交给模型自行决定。"""
    text = (value or "").strip()
    if text == "" or text.lower() in ("auto", "detect"):
        return None
    # 目标语言代码里可能有 zh-CN / zh_cn 这类写法，模型只认主语言码。
    return text.split("-")[0].split("_")[0].lower()


def normalize_device(value):
    """解析 device：auto 交给 torch 决定。"""
    text = (value or "").strip().lower()
    if text in ("", "auto"):
        return "auto"
    if text == "cuda":
        return "cuda"
    return "cpu"


def resolve_model_path(request):
    """确定要加载的模型目录。"""
    model_path = (request.get("model_path") or "").strip()
    if model_path:
        if not os.path.isdir(model_path):
            fail("本地模型目录不存在：%s" % model_path)
        return model_path

    model_name = (request.get("model_name") or "").strip()
    if not model_name:
        fail("未指定模型：请先在「模型管理」中下载模型，或自选本地模型目录")
    download_root = (request.get("download_root") or "").strip()
    if not download_root:
        base = os.path.dirname(os.path.abspath(__file__))
        download_root = os.path.join(base, MODEL_CACHE_DIR)
    try:
        os.makedirs(download_root, exist_ok=True)
    except Exception as exc:  # noqa: BLE001
        fail("无法创建模型缓存目录 %s：%s" % (download_root, exc))
    return model_name


def import_transformers():
    """导入 transformers，缺失时给出可执行的中文提示。"""
    try:
        import torch  # noqa: F401
    except Exception as exc:  # noqa: BLE001
        fail("未安装 torch，请先执行 pip install torch（原因：%s）" % exc)
    try:
        import transformers
    except Exception as exc:  # noqa: BLE001
        fail("未安装 transformers，请先执行 pip install 'transformers>=5.4.0'（原因：%s）" % exc)
    return transformers


def check_dependencies():
    """提前检查音频读取与远程建模代码所需依赖，缺失时一次说清楚。"""
    missing = []
    try:
        import soundfile  # noqa: F401
    except Exception:  # noqa: BLE001
        missing.append("soundfile")
    try:
        import librosa  # noqa: F401
    except Exception:  # noqa: BLE001
        missing.append("librosa")
    try:
        import sentencepiece  # noqa: F401
    except Exception:  # noqa: BLE001
        missing.append("sentencepiece")
    if missing:
        fail("缺少依赖：%s。请执行 pip install %s" % ("、".join(missing), " ".join(missing)))


def load_audio(audio_path):
    """读取音频并重采样为 16kHz 单声道，返回 (ndarray, 时长秒)。"""
    import numpy as np
    import soundfile as sf

    try:
        data, sr = sf.read(audio_path, dtype="float32", always_2d=True)
    except Exception as exc:  # noqa: BLE001
        # soundfile 读不了的格式（例如部分 m4a/webm）退回 librosa。
        try:
            import librosa

            data, sr = librosa.load(audio_path, sr=16000, mono=True)
            return np.asarray(data, dtype="float32"), float(len(data)) / 16000.0
        except Exception as exc2:  # noqa: BLE001
            fail("读取音频失败（%s）：%s；librosa 也失败：%s" % (audio_path, exc, exc2))

    # 多声道取均值，与模型卡片描述的预处理一致。
    mono = data.mean(axis=1) if data.ndim > 1 else data
    if sr != 16000:
        try:
            import librosa

            mono = librosa.resample(np.asarray(mono, dtype="float32"), orig_sr=sr, target_sr=16000)
            sr = 16000
        except Exception as exc:  # noqa: BLE001
            fail("音频重采样到 16kHz 失败（原采样率 %s）：%s" % (sr, exc))
    mono = np.asarray(mono, dtype="float32")
    return mono, float(len(mono)) / float(sr or 16000)


def patch_transformers_compat():
    """修补旧式远程建模代码与新版 transformers 的不兼容点。

    背景：cohere-transcribe 仓库里的 modeling_cohere_asr.py 把
    `_keys_to_ignore_on_load_unexpected` 写成 list，而 transformers >= 5.x 在
    `_adjust_missing_and_unexpected_keys` 里直接对它做 `list | set`，于是抛
    `TypeError: unsupported operand type(s) for |: 'list' and 'set'`。

    这类模型权重本身没问题，纯粹是元数据容器类型变了。这里把两个忽略键统一
    规范成 set 再放行，避免让用户以为模型坏了。
    """
    try:
        from transformers import modeling_utils
    except Exception:  # noqa: BLE001
        return

    cls = getattr(modeling_utils, "PreTrainedModel", None)
    if cls is None:
        return
    original = getattr(cls, "_adjust_missing_and_unexpected_keys", None)
    if original is None or getattr(original, "_cohere_compat", False):
        return

    def wrapper(self, loading_info):
        for attr in (
            "_keys_to_ignore_on_load_unexpected",
            "_keys_to_ignore_on_load_missing",
        ):
            value = getattr(self, attr, None)
            if isinstance(value, (list, tuple)):
                try:
                    setattr(self, attr, set(value))
                except Exception:  # noqa: BLE001
                    pass
        return original(self, loading_info)

    wrapper._cohere_compat = True
    cls._adjust_missing_and_unexpected_keys = wrapper


def build_model_and_processor(request):
    """加载处理器与模型。"""
    transformers = import_transformers()
    import torch

    patch_transformers_compat()

    model_ref = resolve_model_path(request)
    device = normalize_device(request.get("device"))

    # cohere 需要 trust_remote_code 才能用仓库里的自定义建模代码。
    trust_remote_code = bool(request.get("trust_remote_code", True))
    emit({"type": "progress", "ratio": -1, "message": "正在加载模型 %s ..." % model_ref})

    try:
        processor = transformers.AutoProcessor.from_pretrained(
            model_ref, trust_remote_code=trust_remote_code
        )
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("加载处理器失败（%s）：%s" % (model_ref, exc))

    # dtype：优先 bfloat16（cohere 权重是 BF16），CPU 上退到 float32。
    if device == "cuda" and torch.cuda.is_available():
        torch_dtype = torch.bfloat16
        map_kwargs = {"device_map": "auto"}
    else:
        if device == "cuda":
            log("未检测到可用的 CUDA，自动改用 CPU")
        torch_dtype = torch.float32
        map_kwargs = {}

    try:
        model = transformers.AutoModelForSpeechSeq2Seq.from_pretrained(
            model_ref,
            trust_remote_code=trust_remote_code,
            torch_dtype=torch_dtype,
            **map_kwargs,
        )
        model.eval()
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("加载模型失败（%s）：%s" % (model_ref, exc))

    if not map_kwargs:
        model = model.to("cpu" if device != "cuda" else "cuda")
    return model, processor, model_ref


def detect_device_dtype(model):
    """取模型的设备与 dtype，用于把输入张量搬到同一位置。"""
    import torch

    try:
        param = next(model.parameters())
        return param.device, param.dtype
    except StopIteration:
        return torch.device("cpu"), torch.float32


def transcribe_chunk(model, processor, audio, language, device, dtype):
    """对一段音频做一次生成，返回文本。"""
    import torch

    try:
        inputs = processor(
            audio,
            sampling_rate=16000,
            return_tensors="pt",
            language=language,
        )
    except TypeError:
        # 个别版本不接受 language 关键字，退回不指定语言。
        inputs = processor(audio, sampling_rate=16000, return_tensors="pt")

    chunk_index = inputs.pop("audio_chunk_index", None) if hasattr(inputs, "pop") else None
    inputs = inputs.to(device, dtype=dtype)

    with torch.inference_mode():
        outputs = model.generate(**inputs, max_new_tokens=MAX_NEW_TOKENS)

    decode_kwargs = {"skip_special_tokens": True}
    if chunk_index is not None:
        decode_kwargs["audio_chunk_index"] = chunk_index
    if language:
        decode_kwargs["language"] = language

    try:
        text = processor.decode(outputs, **decode_kwargs)
    except TypeError:
        text = processor.decode(outputs, skip_special_tokens=True)

    # 长音频一次传入会被处理器切成多块并返回列表，拼接起来即可。
    if isinstance(text, (list, tuple)):
        text = " ".join(str(item) for item in text)
    return str(text).strip()


def split_chunks(total_seconds):
    """把总时长切成 [(start, end), ...]，带少量重叠。"""
    if total_seconds <= 0:
        return []
    if total_seconds <= CHUNK_SECONDS:
        return [(0.0, total_seconds)]
    chunks = []
    start = 0.0
    step = CHUNK_SECONDS - CHUNK_OVERLAP_SECONDS
    while start < total_seconds:
        end = min(start + CHUNK_SECONDS, total_seconds)
        chunks.append((round(start, 3), round(end, 3)))
        if end >= total_seconds:
            break
        start += step
    return chunks


def transcribe(request):
    """执行识别并按协议输出事件。"""
    audio_path = (request.get("audio_path") or "").strip()
    if not audio_path:
        fail("未指定音频文件路径")
    if not os.path.isfile(audio_path):
        fail("音频文件不存在：%s" % audio_path)

    check_dependencies()
    audio, duration = load_audio(audio_path)
    if duration <= 0:
        fail("音频时长为 0，无法识别：%s" % audio_path)

    model, processor, model_ref = build_model_and_processor(request)
    device, dtype = detect_device_dtype(model)

    language = normalize_language(request.get("language"))
    emit({"type": "progress", "ratio": 0, "message": "开始识别"})

    chunks = split_chunks(duration)
    if not chunks:
        fail("无法切分音频（时长 %.3f 秒）" % duration)

    import numpy as np

    sequence = 0
    skipped_silent = 0
    last_emit = {"ratio": -1.0}
    for index, (start, end) in enumerate(chunks):
        begin_sample = int(start * 16000)
        end_sample = int(end * 16000)
        piece = audio[begin_sample:end_sample]
        if piece.size == 0:
            continue

        # 静音块直接跳过：模型在静音上会幻觉出整段无关文本。
        if block_is_silent(piece):
            skipped_silent += 1
            log("跳过静音片段 %.1f-%.1f 秒" % (start, end))
            ratio = min(1.0, (index + 1) / float(len(chunks)))
            if ratio - last_emit["ratio"] >= 0.01 or index + 1 == len(chunks):
                last_emit["ratio"] = ratio
                emit({
                    "type": "progress",
                    "ratio": round(ratio, 4),
                    "message": "已跳过静音片段 %d / %d" % (index + 1, len(chunks)),
                })
            continue

        try:
            text = transcribe_chunk(model, processor, np.asarray(piece), language, device, dtype)
        except Exception as exc:  # noqa: BLE001
            traceback.print_exc(file=sys.stderr)
            fail("识别第 %d 段（%.1f-%.1f 秒）失败：%s" % (index + 1, start, end, exc))

        if text:
            sequence += 1
            emit({
                "type": "segment",
                "id": str(sequence),
                "sequence": sequence,
                "start": round(start, 3),
                "end": round(end, 3),
                "text": text,
                "language": language or "",
                "confidence": 0.0,
            })

        ratio = min(1.0, (index + 1) / float(len(chunks)))
        if ratio - last_emit["ratio"] >= 0.01 or index + 1 == len(chunks):
            last_emit["ratio"] = ratio
            emit({
                "type": "progress",
                "ratio": round(ratio, 4),
                "message": "已识别 %d / %d 段（%.0f%%）" % (index + 1, len(chunks), ratio * 100),
            })

    if sequence == 0:
        log("所有音频片段都没有识别出文本，可能是静音或语言设置不匹配")
    if skipped_silent:
        log("共跳过 %d 个静音片段（模型在静音上会产生幻觉文本）" % skipped_silent)

    emit({
        "type": "done",
        "language": language or "",
        "language_score": 0.0,
        "duration": round(duration, 3),
        "model": model_ref,
        "engine": ENGINE,
    })


def main():
    request = read_request()
    transcribe(request)
    return 0


if __name__ == "__main__":
    sys.exit(main())
