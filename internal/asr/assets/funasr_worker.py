"""FunASR 识别工作进程（由 Go 侧以子进程方式调用）。

协议与 faster-whisper 工作进程完全一致（JSON Lines）：
    {"type":"progress","ratio":0.42,"message":"..."}
    {"type":"segment","id":"1","sequence":1,"start":0.0,"end":1.2,"text":"...","language":"zh","confidence":0.9}
    {"type":"done","language":"zh","language_score":1.0,"duration":123.4,"model":"...","engine":"funasr"}
    {"type":"error","message":"中文错误"}

约定：
    * stdout 只写 JSON 行，日志写 stderr；
    * 出错时输出 error 事件并以非 0 退出；
    * 优先使用 funasr.AutoModel，导入失败时给出中文说明并退出。
"""

import json
import os
import sys
import traceback

ENGINE = "funasr"
DEFAULT_MODEL = "paraformer-zh"
DEFAULT_VAD_MODEL = "fsmn-vad"


def emit(event):
    sys.stdout.write(json.dumps(event, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def log(message):
    sys.stderr.write(str(message) + "\n")
    sys.stderr.flush()


def fail(message):
    emit({"type": "error", "message": str(message)})
    sys.exit(1)


def read_request():
    raw = sys.argv[1] if len(sys.argv) > 1 else sys.stdin.readline()
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
    text = (value or "").strip()
    if text == "" or text.lower() in ("auto", "detect"):
        return ""
    return text


def map_language_to_funasr(value):
    """把通用语言代码映射为 FunASR 需要的取值。"""
    language = normalize_language(value).lower()
    if language in ("zh", "zh-cn", "zh-tw", "cn", "chinese"):
        return "zh"
    if language in ("en", "english"):
        return "en"
    return language


def build_model(request):
    try:
        from funasr import AutoModel
    except Exception as exc:  # noqa: BLE001
        fail("未安装 FunASR，请先执行 pip install funasr（原因：%s）" % exc)

    model_ref = (request.get("model_path") or "").strip() or (request.get("model_name") or "").strip() or DEFAULT_MODEL
    device = (request.get("device") or "cpu").strip().lower()
    if device not in ("cpu", "cuda"):
        device = "cpu"

    emit({"type": "progress", "ratio": -1, "message": "正在加载 FunASR 模型 %s ..." % model_ref})

    kwargs = {"model": model_ref, "disable_update": True, "device": device, "trust_remote_code": False}
    if not os.path.isdir(model_ref) and "vad" not in model_ref:
        # 使用官方模型名时一并启用 VAD，便于长音频自动切分。
        kwargs["vad_model"] = DEFAULT_VAD_MODEL
    try:
        return AutoModel(**kwargs)
    except TypeError:
        # 旧版本不支持部分参数，退回最小参数集重试。
        try:
            return AutoModel(model=model_ref, disable_update=True)
        except Exception as exc:  # noqa: BLE001
            traceback.print_exc(file=sys.stderr)
            fail("加载 FunASR 模型失败（%s）：%s" % (model_ref, exc))
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("加载 FunASR 模型失败（%s）：%s" % (model_ref, exc))


def extract_sentences(result):
    """从 FunASR 返回结构中提取句子列表。"""
    if not isinstance(result, list):
        return []
    sentences = []
    for item in result:
        if not isinstance(item, dict):
            continue
        text = (item.get("text") or "").strip()
        if text:
            sentences.append({"text": text, "start": 0.0, "end": 0.0})
        for sentence in item.get("sentence_info") or []:
            piece = (sentence.get("text") or "").strip()
            if not piece:
                continue
            sentences.append({
                "text": piece,
                "start": float(sentence.get("start") or 0.0) / 1000.0,
                "end": float(sentence.get("end") or 0.0) / 1000.0,
            })
    return sentences


def transcribe(request):
    audio_path = (request.get("audio_path") or "").strip()
    if not audio_path:
        fail("未指定音频文件路径")
    if not os.path.isfile(audio_path):
        fail("音频文件不存在：%s" % audio_path)

    model = build_model(request)
    language = map_language_to_funasr(request.get("language"))
    emit({"type": "progress", "ratio": 0, "message": "开始识别"})

    kwargs = {"input": audio_path, "batch_size_s": 300}
    if language:
        kwargs["language"] = language
    if request.get("vad_filter"):
        kwargs["vad_model"] = DEFAULT_VAD_MODEL

    try:
        raw_result = model.generate(**kwargs)
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        fail("识别失败：%s" % exc)

    sentences = extract_sentences(raw_result)
    total = float(len(sentences)) or 1.0
    last_end = 0.0
    for index, sentence in enumerate(sentences, start=1):
        emit({
            "type": "segment",
            "id": str(index),
            "sequence": index,
            "start": round(sentence["start"], 3),
            "end": round(sentence["end"], 3),
            "text": sentence["text"],
            "language": language or "zh",
            "confidence": 0.0,
        })
        last_end = max(last_end, sentence["end"])
        emit({"type": "progress", "ratio": round(index / total, 4), "message": "已识别 %d 句" % index})

    emit({
        "type": "done",
        "language": language or "zh",
        "language_score": 1.0 if language else 0.0,
        "duration": round(last_end, 3),
        "model": (request.get("model_name") or request.get("model_path") or DEFAULT_MODEL),
        "engine": ENGINE,
    })


def main():
    transcribe(read_request())
    return 0


if __name__ == "__main__":
    sys.exit(main())
