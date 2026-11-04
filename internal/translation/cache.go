package translation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// Cache 是译文缓存接口。
//
// 实现必须保证并发安全：批处理流水线在并发大于 1 时会在多个 goroutine 中读写缓存。
type Cache interface {
	// Get 读取缓存。第二个返回值表示是否命中。
	Get(key string) (string, bool)
	// Put 写入缓存。空 key 或空值应被实现忽略。
	Put(key, value string)
}

// MemoryCache 是基于内存的并发安全缓存实现，适用于单次翻译任务。
//
// MemoryCache 的零值不可直接使用，请用 NewMemoryCache 构造。
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]string
}

// NewMemoryCache 创建一个空的内存缓存。
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{entries: make(map[string]string)}
}

// Get 读取缓存。
func (c *MemoryCache) Get(key string) (string, bool) {
	if c == nil || key == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entries == nil {
		return "", false
	}
	v, ok := c.entries[key]
	return v, ok
}

// Put 写入缓存。空 key 或空值会被忽略，避免把占位结果污染成命中。
func (c *MemoryCache) Put(key, value string) {
	if c == nil || key == "" || value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]string)
	}
	c.entries[key] = value
}

// Len 返回当前缓存条目数，主要用于测试与诊断。
func (c *MemoryCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Clear 清空缓存。
func (c *MemoryCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]string)
}

// cacheKey 计算一条字幕的缓存键。
//
// 键必须包含源文本、目标语言、Provider/模型标识、风格与内容模式，
// 这样任一会影响译文结果的设置发生变化时缓存都不会误命中。
func cacheKey(providerID, sourceText, sourceLang, targetLang, style, contentMode string) string {
	h := sha256.New()
	// 用不可见分隔符拼接，避免字段边界被源文本伪造造成键碰撞。
	writeField := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0x1f})
	}
	writeField("v1")
	writeField(providerID)
	writeField(sourceText)
	writeField(strings.TrimSpace(sourceLang))
	writeField(strings.TrimSpace(targetLang))
	writeField(strings.TrimSpace(style))
	writeField(strings.TrimSpace(contentMode))
	return hex.EncodeToString(h.Sum(nil))
}
