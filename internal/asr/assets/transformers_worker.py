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
import re
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
# 解码时禁止重复的 n-gram 长度。
#
# 模型陷入解码循环时会把同一短语吐几十上百次（本机实测：日语视频里出现
# "I don't give much." 连写 60 遍、葡萄牙语整句循环）。贪心解码没有采样
# 随机性，一旦进了循环就无法自行跳出，需要在解码层加一道硬约束。
#
# 取值 10 而不是 5：日语一个假名往往就是一个 token（实测 token/字符比
# 只有 0.4~0.9），5-gram 仅覆盖约 6 个字符，会把「ちょっと待ってください。
# ちょっと待ってください。」这类正常的重复呼唤硬改成别的说法。实测对比
# 同一段日语音频：ngram=5 出现「ダメ?」「シール塔」这类明显错字，
# ngram=10 与「无限制」的输出完全一致。取 10 既能覆盖整句循环，又不伤
# 正常文本。
NO_REPEAT_NGRAM_SIZE = 10
# 判定「重复循环」时，重复单元覆盖全文的比例阈值。
LOOP_COVERAGE_THRESHOLD = 0.5
# 重复单元覆盖率达到阈值且单元长度不超过该值时，判为解码循环。
LOOP_MAX_UNIT_CHARS = 200
# 判定为解码循环所需的重复段最短总长度（字符数）。
#
# 这是把「真幻觉」和「正常语气重复」区分开的关键闸门：真实的语气重复
# （"はい。はい。はい。"、"あ。あ。あ。"、"ねえねえねえ"）总长都很短，
# 而解码退化会连吐几十上百次。阈值取 20 与 Go 侧 subtitleclean 一致。
LOOP_MIN_SPAN_CHARS = 20
# 字符级循环：重复单元最大字符数与最少重复次数。
LOOP_CHAR_UNIT_MAX = 4
LOOP_MIN_REPEAT = 3
# 词/句级循环所需的**最少重复次数**（比字符级更严）。
#
# 真实说话会把同一句强调 2~3 次（"ちょっと待って。ちょっと待って。ちょっと待って。"），
# 但极少连说 5 次以上；解码退化则会连吐几十上百次。用次数把两者分开，
# 比单纯看长度更稳：短句连说 3 次是正常语气，长句连说 5 次才是幻觉。
LOOP_MIN_LOOP_REPEAT = 5

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

    # 语言校验尽量提前：加载 2GB 权重要好几十秒，不该等加载完才告诉用户
    # 「语言选错了」。这里用 processor 里已经拿到的 tokenizer 配置做初步
    # 判断，真正的权威校验在 build_decoder_prompt（拿到 model.config 之后）。
    validate_language_early(processor, request.get("language"))

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


def validate_language_early(processor, raw_language):
    """在加载大权重的漫长等待之前，先把「语言选错」这类问题拦下来。

    从 processor 的配置文件里读取模型声明的支持语言列表；读不到就静默跳过，
    交给之后的权威校验（build_decoder_prompt 用 model.config 判断）。
    """
    language = normalize_language(raw_language)
    if language is None:
        return
    try:
        cfg = getattr(processor, "feature_extractor", None)
        supported = getattr(cfg, "supported_languages", None)
        if supported is None:
            tokenizer = getattr(processor, "tokenizer", None)
            supported = getattr(tokenizer, "supported_languages", None)
        if not supported:
            return
        allowed = set(supported)
        if language not in allowed:
            fail(
                "该模型不支持语言 %r，支持：%s。请在任务里改选受支持的源语言"
                % (language, "、".join(sorted(allowed)))
            )
    except SystemExit:
        raise
    except Exception:  # noqa: BLE001
        # 配置里没有该字段是正常的，不要因为校验不了就中断。
        return


def build_decoder_prompt(model, language, punctuation=True):
    """构造 cohere 系模型的解码器 prompt 文本。

    为什么必须这么做：CohereAsrProcessor.__call__ 的签名是
    `(audio, text, sampling_rate, return_tensors, **kwargs)`，多余的
    `language=` 会被 **kwargs 静默吞掉——既不报错也不生效。而模型自己的
    文档写得很清楚：

        language: ISO 639-1 language code. The model does not perform
        language detection, so this is required.

    也就是说，不传语言时 decoder 拿不到任何语言约束，模型只能「猜」，
    在噪声、喘息、音乐段上极易跑成英语/葡语/法语（本机实测：日语视频里
    出现 "I don't give much." ×60、"O que é que você está fazendo?" 等）。
    正确做法是把语言编码进 decoder 的起始 prompt。

    返回 None 表示这个模型不需要/不支持 prompt（例如标准 Whisper），
    调用方应回落到原来的纯音频调用方式。
    """
    build = getattr(model, "build_prompt", None)
    if not callable(build):
        return None
    if not language:
        return None

    # 主动校验语言是否受支持。
    #
    # 不能依赖 build_prompt 自己报错：它只是纯字符串拼接
    # （"<|ja|><|ja|>..."），任何标签都能拼出语法合法的 prompt。
    # 而前端语言列表里有 ru 这类 cohere 并不支持的语言，拼出来的
    # "<|ru|>" 甚至是个合法 token——模型会安静地按错误语言解码，
    # 用户只会看到一堆看不懂的字幕，完全不知道该改语言设置。
    supported = getattr(getattr(model, "config", None), "supported_languages", None)
    if supported:
        allowed = set(supported)
        if language not in allowed:
            fail(
                "该模型不支持语言 %r，支持：%s。请在任务里改选受支持的源语言"
                % (language, "、".join(sorted(allowed)))
            )

    try:
        prompt = build(language=language, punctuation=punctuation)
    except Exception as exc:  # noqa: BLE001
        supported = getattr(getattr(model, "config", None), "supported_languages", None)
        if supported:
            fail("模型不支持语言 %r，支持：%s" % (language, "、".join(sorted(supported))))
        log("构造语言 prompt 失败，将不带语言约束识别（%s）" % exc)
        return None
    if not isinstance(prompt, str) or not prompt.strip():
        return None
    return prompt


def transcribe_chunk(model, processor, audio, language, device, dtype, prompt=None):
    """对一段音频做一次生成，返回文本。"""
    import torch

    call_kwargs = dict(sampling_rate=16000, return_tensors="pt")
    # 关键：语言必须通过 text= 传进 processor，才能变成 decoder_input_ids。
    # 传 language= 会被 processor 的 **kwargs 吞掉，等于没传。
    if prompt:
        call_kwargs["text"] = prompt

    try:
        inputs = processor(audio, **call_kwargs)
    except TypeError:
        # 个别版本不接受 text 关键字，退回不指定语言。
        inputs = processor(audio, sampling_rate=16000, return_tensors="pt")

    chunk_index = inputs.pop("audio_chunk_index", None) if hasattr(inputs, "pop") else None

    # processor 把 prompt 放在 input_ids 里，generate 需要的是 decoder_input_ids。
    if hasattr(inputs, "pop") and "input_ids" in inputs and "decoder_input_ids" not in inputs:
        inputs["decoder_input_ids"] = inputs.pop("input_ids")

    inputs = inputs.to(device, dtype=dtype)

    generate_kwargs = {"max_new_tokens": MAX_NEW_TOKENS}
    # 抑制解码退化：模型陷入循环时会把同一 n-gram 反复吐几十上百次。
    # 对贪心解码（num_beams=1）来说，禁用重复 n-gram 是最直接有效的兜底。
    generate_kwargs["no_repeat_ngram_size"] = NO_REPEAT_NGRAM_SIZE
    generate_kwargs["num_beams"] = 1
    generate_kwargs["do_sample"] = False

    with torch.inference_mode():
        outputs = model.generate(**inputs, **generate_kwargs)

    # 裁掉 decoder 侧喂进去的 prompt，只留模型新生成的 token。
    prompt_len = 0
    if hasattr(inputs, "get"):
        decoder_ids = inputs.get("decoder_input_ids")
        if decoder_ids is not None:
            prompt_len = int(decoder_ids.shape[1])

    decode_kwargs = {"skip_special_tokens": True}
    if chunk_index is not None:
        decode_kwargs["audio_chunk_index"] = chunk_index

    try:
        if prompt_len and hasattr(outputs, "shape") and outputs.shape[1] > prompt_len:
            outputs = outputs[:, prompt_len:]
        text = processor.decode(outputs, **decode_kwargs)
    except TypeError:
        text = processor.decode(outputs, skip_special_tokens=True)

    # 长音频一次传入会被处理器切成多块并返回列表，拼接起来即可。
    if isinstance(text, (list, tuple)):
        text = " ".join(str(item) for item in text)
    return str(text).strip()



def is_degenerate_loop(text):
    """判断文本是否是「同一单元被反复吐出来」的解码退化。

    同时覆盖两种形态：
      1. 词级循环：同一句/同一短语重复几十遍（"I never know. " x 60）；
      2. 字符级循环：同一字符或短单元无空格重复（"あ"x429、"ふん"x150）。
         日语没有词间空格，这类循环按 token 切分只有 1 个 token，必须
         走字符级判定才拦得住。

    关键约束：真实的语气重复（"はい。はい。はい。"、"あ。あ。あ。"）绝不能
    误判，否则会把正常对白整块丢掉——那比幻觉本身更糟。因此要求重复段
    总长度达到 LOOP_MIN_SPAN_CHARS，真实语气重复远达不到这个长度。

    这是生成层的兜底，与 Go 侧 subtitleclean 的清洗互为两道防线。
    """
    if not text:
        return False
    # 整体太短的不可能是解码退化，直接放行（保护短句重复）。
    if len(text) < LOOP_MIN_SPAN_CHARS:
        return False

    if _is_token_loop(text):
        return True
    return _is_char_loop(text)


def _is_token_loop(text):
    """词/句级循环：同一 token 单元连续重复并覆盖大部分文本。"""
    tokens = [t for t in re.split(r"(?<=[.!?。！？])\s*|\s+", text) if t]
    if len(tokens) < 3:
        return False

    total = len(tokens)
    for unit_len in range(1, min(12, total // 2) + 1):
        i = 0
        while i + unit_len * 3 <= total:
            unit = tokens[i:i + unit_len]
            repeat = 1
            while tokens[i + repeat * unit_len: i + (repeat + 1) * unit_len] == unit:
                repeat += 1
            if repeat >= 3:
                coverage = float(repeat * unit_len) / float(total)
                span_chars = len(" ".join(tokens[i:i + unit_len * repeat]))
                # 三重闸门，缺一不可：
                #   1) 覆盖率：重复段要占据大部分文本；
                #   2) 重复段长度：太短的是语气重复，不是幻觉；
                #   3) 重复次数上限：真实说话极少把同一句连说 5 次以上，
                #      而解码退化会连吐几十次。用次数上限把
                #      "ちょっと待って。"x3（30 字，正常强调）和
                #      "I never know."x60（真循环）区分开。
                if (
                    coverage >= LOOP_COVERAGE_THRESHOLD
                    and span_chars >= LOOP_MIN_SPAN_CHARS
                    and repeat >= LOOP_MIN_LOOP_REPEAT
                ):
                    if len(" ".join(unit)) <= LOOP_MAX_UNIT_CHARS:
                        return True
                i += repeat * unit_len
            else:
                i += 1
    return False


def _is_char_loop(text):
    """字符级循环：同一单元（1..4 字符）无间断重复，且总长足够。

    与 Go 侧 subtitleclean.CollapseLongLoops 的判定保持一致，
    这样两道防线对同一份文本的结论不会互相矛盾。
    """
    runes = list(text)
    n = len(runes)
    i = 0
    while i < n:
        for unit_len in range(1, LOOP_CHAR_UNIT_MAX + 1):
            if i + unit_len * LOOP_MIN_REPEAT > n:
                break
            unit = runes[i:i + unit_len]
            repeat = 1
            while runes[i + repeat * unit_len: i + (repeat + 1) * unit_len] == unit:
                repeat += 1
            if repeat >= LOOP_MIN_REPEAT and unit_len * repeat >= LOOP_MIN_SPAN_CHARS:
                return True
        i += 1
    return False


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

    # cohere 这类模型不做语言检测（模型文档原文：The model does not perform
    # language detection, so this is required），没有语言 prompt 时会退化成
    # 在噪声段乱猜语种，产出大量英语/葡语幻觉。所以语言必须显式给出：
    # 缺失时直接失败并告诉用户怎么改，而不是静默产出一堆垃圾字幕。
    if language is None and callable(getattr(model, "build_prompt", None)):
        supported = getattr(getattr(model, "config", None), "supported_languages", None)
        hint = ""
        if supported:
            hint = "。该模型支持：%s" % "、".join(sorted(supported))
        fail(
            "该模型不支持自动检测语言，必须在任务里指定源语言（例如日语选 ja）%s" % hint
        )

    emit({"type": "progress", "ratio": 0, "message": "开始识别"})

    # 语言 prompt 必须在循环外构造一次，避免每块都重建。
    prompt = build_decoder_prompt(model, language)
    if prompt:
        log("使用语言 prompt：%s" % prompt)
    else:
        log("该模型未提供 build_prompt，将不带语言约束识别")

    chunks = split_chunks(duration)
    if not chunks:
        fail("无法切分音频（时长 %.3f 秒）" % duration)

    import numpy as np

    sequence = 0
    skipped_silent = 0
    skipped_loop = 0
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
            text = transcribe_chunk(
                model, processor, np.asarray(piece), language, device, dtype, prompt=prompt
            )
        except Exception as exc:  # noqa: BLE001
            traceback.print_exc(file=sys.stderr)
            fail("识别第 %d 段（%.1f-%.1f 秒）失败：%s" % (index + 1, start, end, exc))

        # 解码循环兜底：即便加了 no_repeat_ngram_size，仍可能整段是同一句重复。
        if text and is_degenerate_loop(text):
            skipped_loop += 1
            log("丢弃疑似解码循环片段 %.1f-%.1f 秒：%s" % (start, end, text[:80]))
            text = ""

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
    if skipped_loop:
        log("共丢弃 %d 个解码循环片段（模型陷入重复输出）" % skipped_loop)

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
