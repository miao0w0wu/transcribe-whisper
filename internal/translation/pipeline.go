package translation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 流水线默认参数。
const (
	defaultBatchSize   = 20
	defaultMaxRetries  = 3
	defaultRetryDelay  = 500 * time.Millisecond
	defaultConcurrency = 1
	maxConcurrency     = 4
	maxBatchSize       = 200
	maxRetryCount      = 10
	maxRetryDelay      = 30 * time.Second
)

// Options 是 Translator 的构造参数。
type Options struct {
	// BatchSize 是每批发给模型的条目数，<=0 时取默认 20（上限 200）。
	BatchSize int
	// MaxRetries 是单批失败后的最大重试次数，<=0 时取默认 3（上限 10）。
	MaxRetries int
	// RetryDelay 是首次重试的等待时间，之后按指数退避（上限 30s）。<=0 时取默认 500ms。
	RetryDelay time.Duration
	// Concurrency 是同时进行的批次数，默认 1（本地模型必须为 1），最大 4。
	Concurrency int
	// Cache 为 nil 时不使用缓存。
	Cache Cache
	// Glossary 是术语表：源语言术语 -> 目标语言译法。
	Glossary map[string]string
	// SourceLanguage 是源语言，可留空表示自动判断。
	SourceLanguage string
	// TargetLanguage 是目标语言。
	TargetLanguage string
	// Style 是语气风格，例如 subtitle / formal / casual。
	Style string
	// ContentMode 是内容模式，例如 subtitle / dialogue / lecture。
	ContentMode string
}

// normalize 把零值或越界参数换成安全默认值。
func (o Options) normalize() Options {
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.BatchSize > maxBatchSize {
		o.BatchSize = maxBatchSize
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = defaultMaxRetries
	}
	if o.MaxRetries > maxRetryCount {
		o.MaxRetries = maxRetryCount
	}
	if o.RetryDelay < 0 {
		o.RetryDelay = 0
	} else if o.RetryDelay == 0 {
		o.RetryDelay = defaultRetryDelay
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.Concurrency > maxConcurrency {
		o.Concurrency = maxConcurrency
	}
	return o
}

// Translator 是字幕批处理翻译流水线。
//
// 职责：分批、重试、缺失补批、缓存、术语表、进度回调与错误聚合。
// Translator 可以重复使用，缓存生命周期由 Options.Cache 决定。
type Translator struct {
	provider Provider
	opts     Options
}

// NewTranslator 构造翻译流水线。
func NewTranslator(provider Provider, opts Options) *Translator {
	return &Translator{provider: provider, opts: opts.normalize()}
}

// Options 返回实际生效的参数，便于调用方展示或测试。
func (t *Translator) Options() Options {
	if t == nil {
		return Options{}
	}
	return t.opts
}

// BatchError 表示部分批次最终失败。
//
// 调用方仍会拿到所有成功翻译的条目，可以先落盘成功部分再提示失败。
type BatchError struct {
	// FailedIDs 是所有最终失败的条目 id，按输入出现顺序去重排列。
	FailedIDs []string
	// Details 是每次失败的描述，便于日志与界面诊断。
	Details []string
	// Err 是最后一次失败的底层错误。
	Err error
}

// Error 实现 error 接口。
func (e *BatchError) Error() string {
	if e == nil {
		return ""
	}
	reason := ""
	if e.Err != nil {
		reason = e.Err.Error()
	} else if len(e.Details) > 0 {
		reason = e.Details[len(e.Details)-1]
	}
	if reason == "" {
		reason = "未知错误"
	}
	ids := strings.Join(e.FailedIDs, ",")
	if len(e.FailedIDs) > 20 {
		ids = strings.Join(e.FailedIDs[:20], ",") + fmt.Sprintf(" 等 %d 条", len(e.FailedIDs))
	}
	return fmt.Sprintf("有 %d 条字幕翻译失败（id：%s），其余条目已正常返回。原因：%s", len(e.FailedIDs), ids, reason)
}

// Unwrap 暴露底层错误，便于 errors.Is / errors.As 判断。
func (e *BatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// batchResult 是一批的翻译结果。
type batchResult struct {
	order   []string
	choices map[string]Choice
	err     error
}

// collected 是翻译过程中的累计结果。
type collected struct {
	mu      sync.Mutex
	items   map[string]Choice
	order   []string
	failed  []string
	details []string
	lastErr error
}

func newCollected() *collected {
	return &collected{items: make(map[string]Choice)}
}

// put 记录成功条目（同一 id 只记第一次）。
func (c *collected) put(cs []Choice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range cs {
		if _, ok := c.items[v.ID]; ok {
			continue
		}
		c.items[v.ID] = v
		c.order = append(c.order, v.ID)
	}
}

// has 判断某 id 是否已有结果。
func (c *collected) has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[id]
	return ok
}

// count 返回已完成的条目数。
func (c *collected) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// fail 记录失败条目。
func (c *collected) fail(ids []string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.failed = appendUnique(c.failed, id)
	}
	if err != nil {
		c.lastErr = err
		c.details = append(c.details, err.Error())
	}
}

// Translate 翻译一组字幕。
//
// 语义约定：
//   - 返回的 []Choice 按输入顺序排列，只包含成功翻译与需要透传的条目，失败条目由 *BatchError.FailedIDs 报告；
//   - 单批失败不会让整次翻译失败，最终以 *BatchError 聚合失败条目；
//   - ctx 取消立即返回 context.Canceled（或 context.DeadlineExceeded），不再重试、不再启动新批次；
//   - progress 在每完成一批（含重试过程中的批次）时回调，参数为已完成条数与总条数。
func (t *Translator) Translate(ctx context.Context, items []Item, progress func(done, total int)) ([]Choice, error) {
	if t == nil || t.provider == nil {
		return nil, &Error{Kind: KindFatal, Message: "翻译流水线未配置 Provider"}
	}
	if err := ctx.Err(); err != nil {
		return nil, cancelError(ctx)
	}
	if len(items) == 0 {
		return []Choice{}, nil
	}

	opts := t.opts
	acc := newCollected()
	total := len(items)

	report := func(done int) {
		if progress == nil {
			return
		}
		if done > total {
			done = total
		}
		if done < 0 {
			done = 0
		}
		progress(done, total)
	}

	// 1. 归一化输入：去空文本、纯符号透传、按 id 去重。
	var passthrough []Choice
	pending := make([]Item, 0, len(items))
	seenIDs := make(map[string]bool, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.ID)
		if id == "" || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		if !needsTranslation(text) {
			passthrough = append(passthrough, Choice{ID: id, Text: text})
			continue
		}
		pending = append(pending, Item{ID: id, Text: text})
	}
	acc.put(passthrough)
	report(acc.count())

	// 2. 缓存命中直接返回，不发给模型。
	needModel := make([]Item, 0, len(pending))
	for _, it := range pending {
		if opts.Cache != nil {
			if v, ok := opts.Cache.Get(t.keyFor(it.Text)); ok && strings.TrimSpace(v) != "" {
				acc.put([]Choice{{ID: it.ID, Text: v}})
				continue
			}
		}
		needModel = append(needModel, it)
	}
	report(acc.count())

	if len(needModel) == 0 {
		return t.finalize(items, acc, nil)
	}

	// 3. 分批；第 0 轮处理全部批次，之后按缺失 id 单独补批（多轮）。
	current := chunkItems(needModel, opts.BatchSize)
	round := 0
	for len(current) > 0 {
		if err := ctx.Err(); err != nil {
			return t.finalize(items, acc, cancelError(ctx))
		}

		roundResults := t.runRound(ctx, current, acc, report)
		if err := ctx.Err(); err != nil {
			return t.finalize(items, acc, cancelError(ctx))
		}

		// 汇总本轮：成功条目入账；失败批次记录错误；返回不完整的剩余条目数按"缺失"统计。
		var missing []Item
		for ri, res := range roundResults {
			batch := current[ri]
			if res.err != nil {
				acc.fail(idsOf(batch), res.err)
				continue
			}
			got := make([]Choice, 0, len(res.order))
			for _, id := range res.order {
				if c, ok := res.choices[id]; ok {
					got = append(got, c)
				}
			}
			acc.put(got)
			if opts.Cache != nil {
				textByID := make(map[string]string, len(batch))
				for _, it := range batch {
					textByID[it.ID] = it.Text
				}
				for _, c := range got {
					if src, ok := textByID[c.ID]; ok {
						opts.Cache.Put(t.keyFor(src), c.Text)
					}
				}
			}
			// 批次内缺条目：收集缺失 id，下一轮单独补批。
			for _, it := range batch {
				if _, ok := res.choices[it.ID]; ok {
					continue
				}
				missing = append(missing, it)
			}
		}

		round++
		if len(missing) == 0 {
			break
		}
		if round > opts.MaxRetries {
			acc.fail(idsOf(missing), &Error{
				Kind:    KindRetryable,
				Message: fmt.Sprintf("模型连续 %d 次未返回完整条目", opts.MaxRetries),
			})
			break
		}
		if delay := retryDelay(opts.RetryDelay, round); delay > 0 {
			if err := sleepCtx(ctx, delay); err != nil {
				return t.finalize(items, acc, cancelError(ctx))
			}
		}
		// 补批只包含缺失条目，且合并成一批（避免"每条一次请求"）。
		current = [][]Item{missing}
	}

	report(total)
	return t.finalize(items, acc, nil)
}

// runRound 执行本轮所有批次，返回与 batches 一一对应的结果。
//
// Concurrency 为 1 时（本地模型的默认与推荐配置）严格按批次顺序串行执行，
// 既避免本地服务被并发拖慢，也保证请求顺序与字幕顺序一致；
// 大于 1 时用信号量限制并发，但结果仍由调用方按原顺序归位。
func (t *Translator) runRound(ctx context.Context, batches [][]Item, acc *collected, progress func(done int)) []batchResult {
	results := make([]batchResult, len(batches))
	var progressMu sync.Mutex

	emit := func() {
		if progress == nil {
			return
		}
		progressMu.Lock()
		done := acc.count()
		progressMu.Unlock()
		progress(done)
	}

	if t.opts.Concurrency <= 1 {
		for i, batch := range batches {
			if err := ctx.Err(); err != nil {
				results[i] = batchResult{err: cancelError(ctx)}
				continue
			}
			results[i] = t.translateBatch(ctx, batch)
			emit()
		}
		return results
	}

	sem := make(chan struct{}, t.opts.Concurrency)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(1)
		go func(idx int, batch []Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				results[idx] = batchResult{err: cancelError(ctx)}
				return
			}
			results[idx] = t.translateBatch(ctx, batch)
			emit()
		}(i, batches[i])
	}
	wg.Wait()
	return results
}

// finalize 按输入顺序整理结果，并在需要时附加术语校验与聚合错误。
func (t *Translator) finalize(items []Item, acc *collected, cancelErr error) ([]Choice, error) {
	acc.mu.Lock()
	byID := acc.items
	failed := append([]string(nil), acc.failed...)
	details := append([]string(nil), acc.details...)
	lastErr := acc.lastErr
	acc.mu.Unlock()

	out := make([]Choice, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	for _, src := range items {
		id := strings.TrimSpace(src.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}

	// 术语后置校验：只做标记与提示，不阻断返回。
	if len(t.opts.Glossary) > 0 && len(out) > 0 {
		src := make([]Item, 0, len(items))
		srcSeen := make(map[string]bool, len(items))
		for _, it := range items {
			if srcSeen[it.ID] {
				continue
			}
			srcSeen[it.ID] = true
			src = append(src, it)
		}
		out, _ = applyGlossary(src, out, t.opts.Glossary)
	}

	if cancelErr != nil {
		if len(out) == 0 {
			return nil, cancelErr
		}
		return out, cancelErr
	}
	if len(failed) == 0 {
		return out, nil
	}
	return out, &BatchError{FailedIDs: failed, Details: details, Err: lastErr}
}

// translateBatch 翻译一批，内部完成重试。
//
// 重试条件：Provider 返回 KindRetryable，或返回内容不满足格式要求（例如无法解析 JSON）。
// KindFatal 与 ctx 取消立即返回，不重试。
//
// 如果 Provider 正常返回但条目不全（模型漏条），本函数只负责补跑缺失的 id：
// 先把拿到的条目交出去，再对缺失部分单独发一次请求，避免整批重来浪费已有的译文。
func (t *Translator) translateBatch(ctx context.Context, batch []Item) batchResult {
	opts := t.opts
	req := Request{
		Items:          batch,
		SourceLanguage: opts.SourceLanguage,
		TargetLanguage: opts.TargetLanguage,
		Style:          opts.Style,
		ContentMode:    opts.ContentMode,
		Glossary:       matchingTerms(batch, opts.Glossary),
	}

	var lastErr error
	// acc 累计本批已经拿到的译文；补跑缺失 id 时不会丢掉前面的成果。
	acc := batchResult{choices: make(map[string]Choice, len(batch))}
	mergeInto := func(src batchResult) {
		for _, id := range src.order {
			c, ok := src.choices[id]
			if !ok {
				continue
			}
			if _, dup := acc.choices[id]; dup {
				continue
			}
			acc.choices[id] = c
			acc.order = append(acc.order, id)
		}
	}

	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return batchResult{err: cancelError(ctx)}
		}

		choices, err := t.provider.Translate(ctx, req)
		if err == nil {
			res := normalizeBatchChoices(req.Items, choices)
			mergeInto(res)
			if len(acc.order) >= len(batch) || len(batch) == 0 {
				return acc
			}
			if len(res.order) > 0 {
				// 部分成功：保留已有结果，只对仍然缺失的 id 再补一次请求。
				missing := missingItems(batch, acc.choices)
				req = Request{
					Items:          missing,
					SourceLanguage: opts.SourceLanguage,
					TargetLanguage: opts.TargetLanguage,
					Style:          opts.Style,
					ContentMode:    opts.ContentMode,
					Glossary:       matchingTerms(missing, opts.Glossary),
				}
				continue
			}
			err = &Error{Kind: KindRetryable, Message: "模型未返回任何有效译文"}
		}

		var provErr *Error
		if errors.As(err, &provErr) && provErr.Kind == KindFatal {
			// 致命错误：已拿到的部分结果照常返回，剩下的条目交由上层记为失败。
			if len(acc.order) > 0 {
				return acc
			}
			// 解码循环：整批被 Ollama 中止。拆成两半分别重试，把肇事条目
			// 隔离出去——一条烂字幕不该让同批另外 19 条一起失败。
			// 递归深度由 len(batch) 收敛，拆到单条即停止。
			if isRepeatLoopError(err) && len(batch) > 1 {
				mid := len(batch) / 2
				left := t.translateBatch(ctx, batch[:mid])
				right := t.translateBatch(ctx, batch[mid:])
				merged := batchResult{choices: make(map[string]Choice, len(batch))}
				for _, part := range []batchResult{left, right} {
					for _, id := range part.order {
						if c, ok := part.choices[id]; ok {
							if _, dup := merged.choices[id]; dup {
								continue
							}
							merged.choices[id] = c
							merged.order = append(merged.order, id)
						}
					}
				}
				if len(merged.order) == 0 {
					if left.err != nil {
						merged.err = left.err
					} else {
						merged.err = right.err
					}
				}
				return merged
			}
			return batchResult{err: err}
		}
		if ctx.Err() != nil {
			return batchResult{err: cancelError(ctx)}
		}

		lastErr = err
		if attempt == opts.MaxRetries {
			break
		}
		if delay := retryDelay(opts.RetryDelay, attempt+1); delay > 0 {
			if sleepErr := sleepCtx(ctx, delay); sleepErr != nil {
				return batchResult{err: cancelError(ctx)}
			}
		}
	}
	if len(acc.order) > 0 {
		return acc
	}
	return batchResult{err: lastErr}
}

// isRepeatLoopError 判断错误是否为「模型解码陷入重复循环被中止」。
//
// 这类错误由 Ollama 以 HTTP 500 + token repeat limit 返回，特征词稳定，
// 用错误文本匹配即可，不需要在 Provider 与流水线之间再加一层错误码。
func isRepeatLoopError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "重复循环") ||
		strings.Contains(msg, "token repeat limit") ||
		strings.Contains(msg, "prediction aborted")
}

// missingItems 返回 batch 里还没有译文的条目。
func missingItems(batch []Item, got map[string]Choice) []Item {
	out := make([]Item, 0, len(batch))
	for _, it := range batch {
		if _, ok := got[it.ID]; ok {
			continue
		}
		out = append(out, it)
	}
	return out
}

// keyFor 生成一条源文本的缓存键。
func (t *Translator) keyFor(sourceText string) string {
	return cacheKey(t.provider.Name(), sourceText, t.opts.SourceLanguage, t.opts.TargetLanguage, t.opts.Style, t.opts.ContentMode)
}

// normalizeBatchChoices 把 Provider 的返回整理成"请求 id -> 译文"，过滤模型臆造的 id。
func normalizeBatchChoices(batch []Item, choices []Choice) batchResult {
	res := batchResult{choices: make(map[string]Choice, len(batch))}
	want := make(map[string]bool, len(batch))
	for _, it := range batch {
		want[it.ID] = true
	}
	for _, c := range choices {
		id := strings.TrimSpace(c.ID)
		text := strings.TrimSpace(c.Text)
		if id == "" || text == "" || !want[id] {
			continue
		}
		if _, dup := res.choices[id]; dup {
			continue
		}
		res.choices[id] = Choice{ID: id, Text: text}
		res.order = append(res.order, id)
	}
	return res
}

// matchingTerms 只把当前批次真正用到的术语传给模型，避免提示词里塞无关内容。
func matchingTerms(batch []Item, glossary map[string]string) map[string]string {
	if len(glossary) == 0 || len(batch) == 0 {
		return nil
	}
	out := make(map[string]string)
	for _, it := range batch {
		for k, v := range glossary {
			if k == "" || strings.TrimSpace(v) == "" {
				continue
			}
			if strings.Contains(it.Text, k) {
				if _, ok := out[k]; !ok {
					out[k] = v
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// idsOf 返回一批条目的 id。
func idsOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// chunkItems 按 size 切分。
func chunkItems(items []Item, size int) [][]Item {
	if size <= 0 {
		size = defaultBatchSize
	}
	out := make([][]Item, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}

// retryDelay 计算第 round 次重试前的等待时间，按指数退避并封顶 30 秒。
func retryDelay(base time.Duration, round int) time.Duration {
	if base <= 0 {
		return 0
	}
	if round <= 1 {
		return base
	}
	d := base
	for i := 1; i < round; i++ {
		if d >= maxRetryDelay {
			return maxRetryDelay
		}
		d *= 2
	}
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

// sleepCtx 可被 ctx 打断的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cancelError 把 ctx 的错误规范成可被 errors.Is 识别的取消错误。
func cancelError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

// appendUnique 追加不重复的 id。
func appendUnique(list []string, id string) []string {
	for _, v := range list {
		if v == id {
			return list
		}
	}
	return append(list, id)
}

// sortStrings 返回排序后的副本（仅用于展示，不改动原切片）。
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TranslateTexts 是便捷方法：把纯文本切片当成字幕翻译，返回与输入等长的译文切片。
//
// 失败条目在结果里保留空字符串；只要有一条失败就返回聚合错误，但仍然带上尽可能多的结果。
func (t *Translator) TranslateTexts(ctx context.Context, texts []string, progress func(done, total int)) ([]string, error) {
	items := make([]Item, len(texts))
	for i, s := range texts {
		items[i] = Item{ID: strconv.Itoa(i), Text: s}
	}
	choices, err := t.Translate(ctx, items, progress)
	out := make([]string, len(texts))
	for _, c := range choices {
		idx, convErr := strconv.Atoi(c.ID)
		if convErr != nil || idx < 0 || idx >= len(out) {
			continue
		}
		out[idx] = c.Text
	}
	return out, err
}
