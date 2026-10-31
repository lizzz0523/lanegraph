package async

import (
	"context"
	"sync"
)

// Config 描述一个 Loader 的取值方式与 UI 回调。
type Config[T, K any] struct {
	Key  func(K) string
	Load func(context.Context, K) (T, error)
	// Post 把闭包投递到 UI 线程，只能由后台 goroutine 调用。
	Post      func(func())      // 为 nil 时 OnResult 在后台 goroutine 调用
	OnLoading func(K)           // UI 线程
	OnResult  func(K, T, error) // Post 非 nil 时在 UI 线程
}

// phase 是当前槽位的结果状态。
type phase uint8

const (
	phaseIdle phase = iota
	phaseLoading
	phaseShown
	phaseFailed
)

// slot 是 Loader 当前关心的唯一请求。
type slot[K any] struct {
	arg    K
	key    string
	phase  phase
	cancel context.CancelFunc
}

// Loader 是一个单槽位的异步取值器：同一时间只关心最新一次请求。
// 缓存命中时同步返回；未命中时在后台加载，过期的成功结果只写缓存、不回写 UI。
type Loader[T, K any] struct {
	cfg Config[T, K]

	mu    sync.Mutex
	cache map[string]entry[T]
	slot  slot[K]
	seq   uint64

	started bool
	closed  bool

	wake chan struct{}
	done chan struct{}
}

type entry[T any] struct {
	value T
	err   error
}

func New[T, K any](cfg Config[T, K]) *Loader[T, K] {
	return &Loader[T, K]{
		cfg:   cfg,
		cache: make(map[string]entry[T]),
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// Loading 表示当前槽位仍在等待结果或等待结果交付。
func (l *Loader[T, K]) Loading() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.slot.phase == phaseLoading
}

// Request 请求 arg 对应的值。只能在 UI 线程调用。
func (l *Loader[T, K]) Request(arg K) {
	key := l.cfg.Key(arg)

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	if key == l.slot.key {
		if cached, ok := l.cache[key]; ok {
			if l.slot.phase == phaseShown {
				l.mu.Unlock()
				return
			}
			l.slot.phase = phaseShown
			l.mu.Unlock()
			l.result(arg, cached)
			return
		}
		// 没有缓存：只有上次请求失败才重试，加载中/空闲直接返回。
		if l.slot.phase != phaseFailed {
			l.mu.Unlock()
			return
		}
	}

	l.slot.arg = arg
	l.slot.key = key
	l.slot.phase = phaseLoading
	l.seq++

	cancel := l.slot.cancel
	l.slot.cancel = nil
	cached, ok := l.cache[key]
	if ok {
		l.slot.phase = phaseShown
	}
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if ok {
		l.result(arg, cached)
		return
	}

	l.startLoad(arg)
}

// Reload 丢弃 arg 对应的缓存并重新请求，用于切换面板时强制刷新。
// 只能在 UI 线程调用。
func (l *Loader[T, K]) Reload(arg K) {
	key := l.cfg.Key(arg)

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	// 在锁内取消在途请求，确保旧结果不会在删除缓存后重新写回；
	// 否则 Request 可能命中刚刚写回的旧缓存。
	l.cancelLocked()
	delete(l.cache, key)
	// 之前请求的就是同一个 key 时，Request 会把它当成重复请求直接返回，
	// 因此这里先让槽位失效，让下面的 Request 真正发起加载。
	if l.slot.key == key {
		l.resetLocked()
	}
	l.mu.Unlock()

	l.Request(arg)
}

// Reset 丢弃全部缓存并取消在途请求，让后续 Request 重新加载。
// 用于数据源整体变化、无法按 key 判断失效的场景。
// 只能在 UI 线程调用。
func (l *Loader[T, K]) Reset() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.cancelLocked()
	l.cache = make(map[string]entry[T])
	l.resetLocked()
	l.mu.Unlock()
}

// Cancel 让当前在途结果失效，并取消当前 Load 的 context。
func (l *Loader[T, K]) Cancel() {
	l.mu.Lock()
	l.cancelLocked()
	l.resetLocked()
	l.mu.Unlock()
}

func (l *Loader[T, K]) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.done)
	cancel := l.slot.cancel
	l.slot.cancel = nil
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// resetLocked 让当前槽位失效，并让在途结果的 seq 过期。
func (l *Loader[T, K]) resetLocked() {
	l.slot.key = ""
	l.slot.phase = phaseIdle
	l.seq++
}

// cancelLocked 取消当前在途加载。
func (l *Loader[T, K]) cancelLocked() {
	if l.slot.cancel != nil {
		l.slot.cancel()
		l.slot.cancel = nil
	}
}

// startLoad 通知 UI 并唤醒后台加载。
func (l *Loader[T, K]) startLoad(arg K) {
	if l.cfg.OnLoading != nil {
		l.cfg.OnLoading(arg)
	}
	l.start()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Loader[T, K]) start() {
	l.mu.Lock()
	if l.started || l.closed {
		l.mu.Unlock()
		return
	}
	l.started = true
	l.mu.Unlock()
	go l.loop()
}

func (l *Loader[T, K]) loop() {
	for {
		select {
		case <-l.done:
			return
		case <-l.wake:
		}

		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		arg, key, seq := l.slot.arg, l.slot.key, l.seq
		if key == "" {
			l.mu.Unlock()
			continue
		}
		if cached, ok := l.cache[key]; ok {
			l.mu.Unlock()
			l.deliver(arg, key, seq, cached)
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		l.slot.cancel = cancel
		l.mu.Unlock()

		value, err := l.cfg.Load(ctx, arg)

		l.mu.Lock()
		l.slot.cancel = nil
		canceled := ctx.Err() != nil
		if !canceled && err == nil {
			l.cache[key] = entry[T]{value: value}
		}
		l.mu.Unlock()
		cancel()

		if canceled {
			continue
		}
		l.deliver(arg, key, seq, entry[T]{value: value, err: err})
	}
}

func (l *Loader[T, K]) deliver(arg K, key string, seq uint64, value entry[T]) {
	apply := func() {
		l.mu.Lock()
		if l.closed || seq != l.seq || key != l.slot.key || l.slot.phase == phaseShown {
			l.mu.Unlock()
			return
		}
		if value.err != nil {
			l.slot.phase = phaseFailed
		} else {
			l.slot.phase = phaseShown
		}
		l.mu.Unlock()
		l.result(arg, value)
	}

	if l.cfg.Post != nil {
		l.cfg.Post(apply)
		return
	}
	apply()
}

func (l *Loader[T, K]) result(arg K, value entry[T]) {
	if l.cfg.OnResult != nil {
		l.cfg.OnResult(arg, value.value, value.err)
	}
}
