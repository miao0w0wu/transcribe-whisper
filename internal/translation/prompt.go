package translation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// promptInput 是发给模型的单条输入条目。
type promptInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// buildSystemPrompt 构造系统提示。
//
// 系统提示固定为中文，负责说明任务、输出格式、语气风格与术语表；
// 不含任何字幕正文，正文一律放在 user 消息里。
func buildSystemPrompt(req Request) string {
	var b strings.Builder

	b.WriteString("你是一名专业的字幕翻译引擎。请把用户给出的 JSON 数组中的每一条字幕翻译成")
	b.WriteString(languageName(req.TargetLanguage))
	b.WriteString("。\n\n")

	b.WriteString("## 输出要求\n")
	b.WriteString("1. 只输出 JSON，不要输出任何解释、注释、Markdown 代码块标记或其他文字。\n")
	b.WriteString("2. 推荐形态是一个 JSON 对象，键是原 id，值是译文，例如 {\"1\": \"译文一\", \"2\": \"译文二\"}。\n")
	b.WriteString("3. 也可以输出数组，元素为 {\"id\": \"原 id\", \"text\": \"译文\"}。两种形态任选其一，不要混用。\n")
	b.WriteString("4. 必须原样保留每条的 id，不能改写、增补、合并或遗漏任何一条，输入有几条输出就是几条。\n")
	b.WriteString("5. id 保持为字符串，即使是纯数字也不要转成数字类型。\n")
	b.WriteString("6. 译文只能是译文本身，不要带引号、序号、时间轴或说话人标注（原文已有的除外）。\n\n")

	b.WriteString("## 语气与风格\n")
	b.WriteString(styleGuide(req.Style))
	b.WriteString("\n")
	b.WriteString(contentModeGuide(req.ContentMode))
	b.WriteString("\n")

	b.WriteString("## 通用约束\n")
	b.WriteString("- 不要因为句子太短、像语气词或无意义就跳过，任何一条都要给出译文。\n")
	b.WriteString("- 数字、单位、专有名词、代码、变量名在被翻译语言中保持原样。\n")
	b.WriteString("- 原文中的换行请用 \\n 表示，不要在 JSON 里写真实换行导致格式非法。\n")

	if len(req.Glossary) > 0 {
		b.WriteString("\n## 术语表（必须严格遵守）\n")
		b.WriteString("下面每一行的术语在译文中必须使用指定译法，不得自行意译或替换：\n")
		for _, k := range sortedKeys(req.Glossary) {
			v := strings.TrimSpace(req.Glossary[k])
			if v == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(k)
			b.WriteString(" => ")
			b.WriteString(v)
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// buildUserPrompt 构造 user 消息，只包含待翻译条目的 JSON 数组。
func buildUserPrompt(req Request) (string, error) {
	inputs := make([]promptInput, 0, len(req.Items))
	for _, it := range req.Items {
		inputs = append(inputs, promptInput{ID: it.ID, Text: it.Text})
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", &Error{Kind: KindFatal, Message: "构造翻译请求失败", Err: err}
	}
	return "待翻译字幕：\n" + string(data), nil
}

// buildPrompt 返回系统提示与用户消息，供各 Provider 复用。
func buildPrompt(req Request) (system, user string, err error) {
	user, err = buildUserPrompt(req)
	if err != nil {
		return "", "", err
	}
	return buildSystemPrompt(req), user, nil
}

// styleGuide 把风格参数转成对模型的具体要求。
func styleGuide(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "", "subtitle", "字幕":
		return "- 风格：字幕。译文简洁、口语化、贴近人说话的方式；单条尽量短，方便观众一眼读完，不要加书面化的修饰语。"
	case "formal", "正式":
		return "- 风格：正式。用词规范、书面化，避免网络用语和随意的省略。"
	case "casual", "口语", "轻松":
		return "- 风格：口语。自然随性，允许使用目标语言中常见的口头表达。"
	case "literal", "直译":
		return "- 风格：直译。尽量贴近原文语序与结构，不做过度的本地化改写。"
	case "explicit", "露骨", "成人":
		return strings.Join([]string{
			"- 风格：露骨。这是成人向影音内容的字幕翻译，请直白、如实地翻译原文的性相关内容，不要淡化、回避或省略。",
			"- 涉及性行为、身体部位与生理描述的词汇，使用目标语言中成人内容约定俗成的直白说法，不要用委婉语、学术代称或「那个」之类的模糊表达。",
			"- 原文中的呻吟、喘息、脏话、称呼（如 宝贝 / 亲爱的 / 主人）按原意如实译出，保留脏话的粗俗程度，不要净化或雅化。",
			"- 保持说话人的语气、情绪强度与亲密/服从关系，不要改成书面叙述。",
			"- 仍然只输出译文本身，不要添加任何警告、说明或内容提示。",
		}, "\n")
	case "free", "意译":
		return "- 风格：意译。优先表达原意与语气，可以重组句子结构使译文更自然。"
	default:
		return "- 风格：" + strings.TrimSpace(style) + "。请保持与之匹配的语气。"
	}
}

// contentModeGuide 把内容模式转成对模型的具体要求。
func contentModeGuide(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "subtitle", "字幕":
		return "- 内容：影视/视频字幕。按字幕的阅读习惯断句，保留情绪与语气。"
	case "dialogue", "对话":
		return "- 内容：对话。保留说话人的语气与身份差异，不要合并不同说话人的话。"
	case "lecture", "讲座", "课程":
		return "- 内容：讲座/课程。术语要准确一致，允许比字幕更完整的表达。"
	case "meeting", "会议":
		return "- 内容：会议记录。用清楚、结论化的表达，不要遗漏决策与数字。"
	default:
		return "- 内容：" + strings.TrimSpace(mode) + "。请按该场景的表达习惯翻译。"
	}
}

// languageName 把语言代码或名称规范化成给模型看的中文描述。
func languageName(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return "目标语言"
	}
	if name, ok := languageAliases[strings.ToLower(lang)]; ok {
		return name
	}
	return `"` + lang + `"`
}

// languageAliases 覆盖前端常见的语言代码与中文名。
var languageAliases = map[string]string{
	"zh":       "简体中文",
	"zh-cn":    "简体中文",
	"zh_cn":    "简体中文",
	"zh-hans":  "简体中文",
	"chinese":  "简体中文",
	"cn":       "简体中文",
	"中文":       "简体中文",
	"简体中文":     "简体中文",
	"zh-tw":    "繁体中文",
	"zh-hant":  "繁体中文",
	"繁体中文":     "繁体中文",
	"en":       "英语",
	"english":  "英语",
	"英语":       "英语",
	"ja":       "日语",
	"jp":       "日语",
	"japanese": "日语",
	"日语":       "日语",
	"ko":       "韩语",
	"korean":   "韩语",
	"韩语":       "韩语",
	"fr":       "法语",
	"french":   "法语",
	"de":       "德语",
	"german":   "德语",
	"es":       "西班牙语",
	"spanish":  "西班牙语",
	"ru":       "俄语",
	"russian":  "俄语",
	"pt":       "葡萄牙语",
	"italian":  "意大利语",
	"ar":       "阿拉伯语",
	"th":       "泰语",
	"vi":       "越南语",
	"auto":     "自动检测出的语言",
	"自动":       "自动检测出的语言",
}

// sortedKeys 返回排序后的非空 map 键，保证提示词与测试输出稳定。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if k == "" || strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// choiceErrorf 构造一条可重试的格式错误，信息里带原因但绝不能包含完整字幕内容。
func choiceErrorf(format string, args ...any) *Error {
	return &Error{Kind: KindRetryable, Message: "模型返回格式不符合要求：" + fmt.Sprintf(format, args...)}
}

// maxResponseForParse 限制解析时最多处理多少字节，避免异常响应撑爆内存。
const maxResponseForParse = 4 << 20

// parseChoices 解析模型返回的译文。
//
// 模型实际会给出多种合法形态，全部都要能吃下：
//  1. JSON 数组：[{"id":"1","text":"译文"}]（提示词要求的主形态）
//  2. id 到译文的映射：{"1":"译文"}（Ollama 的 format=json 会强制输出对象，
//     模型最自然的选择就是这个形态）
//  3. 被包装的对象：{"translations":[{"id":"1","text":"译文"}]}
//
// 容错策略：先剥掉 ``` 代码块，再按括号配对取出第一个完整的 JSON 值，
// 因此前后夹带的解释文字不会影响解析。缺 id、text 为空都视为格式错误，
// 返回可重试错误。
func parseChoices(raw string) ([]Choice, error) {
	if len(raw) > maxResponseForParse {
		return nil, choiceErrorf("响应过长（%d 字节），已放弃解析", len(raw))
	}
	s := stripCodeFence(raw)

	// 形态一：数组。优先尝试，因为它是提示词要求的主形态。
	if segment, ok := extractJSONValue(s, '[', ']'); ok {
		if choices, err := decodeChoiceArray(segment); err == nil {
			return choices, nil
		} else if !shouldFallbackToObject(err) {
			return nil, err
		}
	}

	// 形态二/三：对象。
	if segment, ok := extractJSONValue(s, '{', '}'); ok {
		if choices, err := decodeChoiceObject(segment); err == nil {
			return choices, nil
		} else {
			// 对象解析失败时，把真正的格式问题抛出去，而不是笼统报「未找到数组」。
			return nil, err
		}
	}

	if _, ok := extractJSONValue(s, '[', ']'); ok {
		return nil, choiceErrorf("JSON 数组内容不合法")
	}
	return nil, choiceErrorf("未找到合法的 JSON 数组或对象")
}

// shouldFallbackToObject 判断数组解析失败后是否值得再试对象形态。
//
// 数组存在但内容缺字段时不必再试对象（那是模型确实漏条目，重试更有意义）；
// 只有类型不匹配（例如对象被当成数组）才回退。
func shouldFallbackToObject(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "JSON 解析失败") || strings.Contains(msg, "数组为空")
}

// decodeChoiceArray 解析 [{"id":..,"text":..}] 形态。
func decodeChoiceArray(segment string) ([]Choice, error) {
	var parsed []Choice
	if err := json.Unmarshal([]byte(segment), &parsed); err != nil {
		return nil, choiceErrorf("JSON 解析失败（%s）", scrubJSONError(err))
	}
	if len(parsed) == 0 {
		return nil, choiceErrorf("返回的数组为空")
	}
	return normalizeChoices(parsed)
}

// reservedObjectKeys 是结构字段名。
//
// 如果模型返回的对象里出现这些键，说明它给的是一条（或几条）翻译记录的包装结构，
// 而不是 id 到译文的映射；此时不能把它误当成映射，否则未闭合的数组
// （例如 `[{"id":"1","text":"甲"}`）会被错读成 id="id"、id="text" 两条译文。
var reservedObjectKeys = map[string]bool{
	"id":          true,
	"text":        true,
	"translation": true,
	"translated":  true,
	"source":      true,
	"sequence":    true,
	"segments":    true,
	"subtitles":   true,
}

// decodeChoiceObject 解析对象的两种形态：包装数组，或 id 到译文的映射。
func decodeChoiceObject(segment string) ([]Choice, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(segment), &wrapper); err != nil {
		return nil, choiceErrorf("JSON 解析失败（%s）", scrubJSONError(err))
	}
	if len(wrapper) == 0 {
		return nil, choiceErrorf("返回的对象为空")
	}

	// 形态三：对象里挂着数组字段，例如 {"translations":[...]}。
	for _, value := range wrapper {
		trimmed := strings.TrimSpace(string(value))
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		if choices, err := decodeChoiceArray(trimmed); err == nil {
			return choices, nil
		}
	}

	// 结构字段名的对象不是 id->译文 映射，属于模型没有按约定输出。
	for key := range wrapper {
		if reservedObjectKeys[strings.ToLower(strings.TrimSpace(key))] {
			return nil, choiceErrorf("返回的是单条记录而不是 id 到译文的映射")
		}
	}

	// 形态二：id -> 译文。值必须是字符串。
	choices := make([]Choice, 0, len(wrapper))
	for rawID, value := range wrapper {
		trimmed := strings.TrimSpace(string(value))
		var text string
		if err := json.Unmarshal([]byte(trimmed), &text); err != nil {
			// 值不是字符串（例如嵌套对象）说明不是这个形态。
			return nil, choiceErrorf("对象里的 id=%s 对应的值不是译文文本", rawID)
		}
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, choiceErrorf("对象里存在空 id")
		}
		if strings.TrimSpace(text) == "" {
			return nil, choiceErrorf("id=%s 的 text 为空", id)
		}
		choices = append(choices, Choice{ID: id, Text: strings.TrimSpace(text)})
	}
	// map 遍历顺序随机，排序后让同一次响应在测试与日志里稳定。
	sort.Slice(choices, func(i, j int) bool { return choices[i].ID < choices[j].ID })
	return choices, nil
}

// normalizeChoices 校验并清理数组形态的结果。
func normalizeChoices(parsed []Choice) ([]Choice, error) {
	out := make([]Choice, 0, len(parsed))
	for idx, c := range parsed {
		id := strings.TrimSpace(c.ID)
		text := strings.TrimSpace(c.Text)
		if id == "" {
			return nil, choiceErrorf("第 %d 条缺少 id", idx+1)
		}
		if text == "" {
			return nil, choiceErrorf("id=%s 的 text 为空", id)
		}
		out = append(out, Choice{ID: id, Text: text})
	}
	return out, nil
}

// extractJSONValue 用括号配对从任意文本里取出第一个完整的 JSON 数组或对象。
func extractJSONValue(raw string, open, close byte) (string, bool) {
	start := strings.IndexByte(raw, open)
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return strings.TrimSpace(raw[start : i+1]), true
			}
		}
	}
	return "", false
}

// stripCodeFence 去掉 Markdown 代码块包裹。
func stripCodeFence(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "```") {
		return s
	}
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```JSON", "")
	s = strings.ReplaceAll(s, "```Json", "")
	s = strings.ReplaceAll(s, "```", "")
	return strings.TrimSpace(s)
}

// scrubJSONError 把 json 包的错误压缩成简短描述，避免把整段响应带进错误信息。
func scrubJSONError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "invalid character"); i >= 0 {
		msg = msg[i:]
	}
	if len(msg) > 160 {
		msg = msg[:160] + "..."
	}
	return msg
}
