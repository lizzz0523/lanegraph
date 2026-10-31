package async

import "container/list"

const defaultCacheSize = 64

// entry 是一次加载结果，成功和失败共用。
type entry[T any] struct {
	value T
	err   error
}

type cacheItem[T any] struct {
	key   string
	entry entry[T]
}

// cache 是带 LRU 上限的缓存，调用方必须自行保证并发安全。
type cache[T any] struct {
	capacity int
	items    map[string]*list.Element
	lru      *list.List
}

func newCache[T any](capacity int) *cache[T] {
	if capacity <= 0 {
		capacity = defaultCacheSize
	}
	return &cache[T]{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		lru:      list.New(),
	}
}

func (c *cache[T]) Get(key string) (entry[T], bool) {
	elem, ok := c.items[key]
	if !ok {
		var zero entry[T]
		return zero, false
	}
	c.lru.MoveToFront(elem)
	return elem.Value.(*cacheItem[T]).entry, true
}

func (c *cache[T]) Put(key string, value entry[T]) {
	if elem, ok := c.items[key]; ok {
		item := elem.Value.(*cacheItem[T])
		item.entry = value
		c.lru.MoveToFront(elem)
		return
	}

	elem := c.lru.PushFront(&cacheItem[T]{key: key, entry: value})
	c.items[key] = elem

	if c.lru.Len() > c.capacity {
		c.removeOldest()
	}
}

func (c *cache[T]) Delete(key string) {
	elem, ok := c.items[key]
	if !ok {
		return
	}
	item := elem.Value.(*cacheItem[T])
	delete(c.items, item.key)
	c.lru.Remove(elem)
}

func (c *cache[T]) Clear() {
	c.items = make(map[string]*list.Element)
	c.lru.Init()
}

func (c *cache[T]) removeOldest() {
	elem := c.lru.Back()
	if elem == nil {
		return
	}
	item := elem.Value.(*cacheItem[T])
	delete(c.items, item.key)
	c.lru.Remove(elem)
}
