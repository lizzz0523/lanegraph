package async

import (
	"context"
	"sync"
)

// Config 描述一个 Task 的执行方式与 UI 回调。
type Config[T, K any] struct {
	Key func(K) string
	Run func(context.Context, K) (T, error)
	// Post 把闭包投递到 UI 线程，只能由后台 goroutine 调用。
	Post      func(func())      // 为 nil 时 OnResult 在后台 goroutine 调用
	OnRunning func(K)           // UI 线程
	OnResult  func(K, T, error) // Post 非 nil 时在 UI 线程
	// NoCache 为 true 时不读写缓存，Execute 每次都会重新执行；适合操作类任务。
	NoCache bool
}

// phase 是当前槽位的结果状态。
type phase uint8

const (
	phaseIdle phase = iota
	phaseRunning
	phaseShown
	phaseFailed
)

// slot 是 Task 当前关心的唯一请求。
type slot[K any] struct {
	arg    K
	key    string
	phase  phase
	cancel context.CancelFunc
}

// Lifecycle 统一描述 Task 的关闭与等待操作，便于管理不同泛型实例。
type Lifecycle interface {
	Close()
	Wait()
}

// Task 是一个单槽位的异步任务：同一时间只关心最新一次请求。
// 缓存命中时同步返回；未命中时在后台执行，过期的成功结果只写缓存、不回写 UI。
type Task[T, K any] struct {
	cfg Config[T, K]

	mu    sync.Mutex
	cache *cache[T]
	slot  slot[K]
	seq   uint64

	started bool
	closed  bool
	loopWG  sync.WaitGroup

	wake chan struct{}
	done chan struct{}
}

func New[T, K any](cfg Config[T, K]) *Task[T, K] {
	return &Task[T, K]{
		cfg:   cfg,
		cache: newCache[T](defaultCacheSize),
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// Running 表示当前槽位仍在等待结果或等待结果交付。
func (t *Task[T, K]) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.slot.phase == phaseRunning
}

// Execute 确保 arg 对应的结果可用：缓存命中时直接返回，否则在后台执行。
// NoCache 任务没有缓存可用，每次调用都会重新执行。只能在 UI 线程调用。
func (t *Task[T, K]) Execute(arg K) {
	key := t.cfg.Key(arg)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if key == t.slot.key && !t.cfg.NoCache {
		if cached, ok := t.cache.Get(key); ok {
			if t.slot.phase == phaseShown {
				t.mu.Unlock()
				return
			}
			t.slot.phase = phaseShown
			t.mu.Unlock()
			t.result(arg, cached)
			return
		}
		// 没有缓存：只有上次请求失败才重试，执行中/空闲直接返回。
		if t.slot.phase != phaseFailed {
			t.mu.Unlock()
			return
		}
	}

	t.slot.arg = arg
	t.slot.key = key
	t.slot.phase = phaseRunning
	t.seq++

	cancel := t.slot.cancel
	t.slot.cancel = nil
	cached, ok := t.cache.Get(key)
	if ok {
		t.slot.phase = phaseShown
	}
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if ok {
		t.result(arg, cached)
		return
	}

	t.startTask(arg)
}

// Reset 丢弃全部缓存并取消在途请求，让后续 Execute 重新执行。
// 用于数据源整体变化、无法按 key 判断失效的场景。
// 只能在 UI 线程调用。
func (t *Task[T, K]) Reset() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.cancelLocked()
	t.cache.Clear()
	t.resetLocked()
	t.mu.Unlock()
}

// Cancel 让当前在途结果失效，并取消当前 Run 的 context。
func (t *Task[T, K]) Cancel() {
	t.mu.Lock()
	t.cancelLocked()
	t.resetLocked()
	t.mu.Unlock()
}

func (t *Task[T, K]) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	cancel := t.slot.cancel
	t.slot.cancel = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Wait 等待后台 loop 退出。调用前应先 Close，否则可能一直阻塞。
func (t *Task[T, K]) Wait() {
	t.loopWG.Wait()
}

// resetLocked 让当前槽位失效，并让在途结果的 seq 过期。
func (t *Task[T, K]) resetLocked() {
	t.slot.key = ""
	t.slot.phase = phaseIdle
	t.seq++
}

// cancelLocked 取消当前在途执行。
func (t *Task[T, K]) cancelLocked() {
	if t.slot.cancel != nil {
		t.slot.cancel()
		t.slot.cancel = nil
	}
}

// startTask 通知 UI 并唤醒后台执行。
func (t *Task[T, K]) startTask(arg K) {
	if t.cfg.OnRunning != nil {
		t.cfg.OnRunning(arg)
	}
	t.start()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *Task[T, K]) start() {
	t.mu.Lock()
	if t.started || t.closed {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.loopWG.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.loopWG.Done()
		t.loop()
	}()
}

func (t *Task[T, K]) loop() {
	for {
		select {
		case <-t.done:
			return
		case <-t.wake:
		}

		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		arg, key, seq := t.slot.arg, t.slot.key, t.seq
		if key == "" {
			t.mu.Unlock()
			continue
		}
		if cached, ok := t.cache.Get(key); ok {
			t.mu.Unlock()
			t.deliver(arg, key, seq, cached)
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		t.slot.cancel = cancel
		t.mu.Unlock()

		value, err := t.cfg.Run(ctx, arg)

		t.mu.Lock()
		t.slot.cancel = nil
		canceled := ctx.Err() != nil
		if !canceled && err == nil && !t.cfg.NoCache {
			t.cache.Put(key, entry[T]{value: value})
		}
		t.mu.Unlock()
		cancel()

		if canceled {
			continue
		}
		t.deliver(arg, key, seq, entry[T]{value: value, err: err})
	}
}

func (t *Task[T, K]) deliver(arg K, key string, seq uint64, value entry[T]) {
	apply := func() {
		t.mu.Lock()
		if t.closed || seq != t.seq || key != t.slot.key || t.slot.phase == phaseShown {
			t.mu.Unlock()
			return
		}
		if value.err != nil {
			t.slot.phase = phaseFailed
		} else {
			t.slot.phase = phaseShown
		}
		t.mu.Unlock()
		t.result(arg, value)
	}

	if t.cfg.Post != nil {
		t.cfg.Post(apply)
		return
	}
	apply()
}

func (t *Task[T, K]) result(arg K, value entry[T]) {
	if t.cfg.OnResult != nil {
		t.cfg.OnResult(arg, value.value, value.err)
	}
}
