package cold

import (
	"sync"
	"time"
)

// WalValve bounds WAL growth while autocheckpoint stays deferred. Growth
// tracks walSize minus baseline, since WAL never shrinks after wrap.
const (
	defaultWalValveMB = 256
	hardCapMultiplier = 2
	fileCapMultiplier = 4
	checkInterval     = 2 * time.Second
	maxPausedPasses   = 20
)

type WalValve struct {
	vault *Vault

	softBytes    int64
	hardBytes    int64
	fileCapBytes int64

	mu       sync.Mutex
	baseline int64
	ticker   *time.Ticker
	pauseCh  chan struct{}
	stopCh   chan struct{}
}

// NewWalValve creates a valve for v with thresholds scaled from DB size.
func NewWalValve(v *Vault) *WalValve {
	softMB := resolveWalValveMB(v)
	soft := int64(softMB) * 1024 * 1024
	return &WalValve{
		vault:        v,
		softBytes:    soft,
		hardBytes:    soft * hardCapMultiplier,
		fileCapBytes: soft * fileCapMultiplier,
		baseline:     v.GetWalSizeBytes(),
		stopCh:       make(chan struct{}),
	}
}

func resolveWalValveMB(v *Vault) int {
	dbSize := v.GetDbFileSizeBytes()
	if dbSize <= 0 {
		return defaultWalValveMB
	}
	mb := int(dbSize / (4 * 1024 * 1024))
	if mb < defaultWalValveMB {
		mb = defaultWalValveMB
	}
	if mb > 2048 {
		mb = 2048
	}
	return mb
}

// Start begins periodic passive checkpoints past the soft limit. Idempotent.
func (w *WalValve) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ticker != nil {
		return
	}
	w.baseline = w.vault.GetWalSizeBytes()
	w.ticker = time.NewTicker(checkInterval)
	go func() {
		for {
			select {
			case <-w.ticker.C:
				w.check()
			case <-w.stopCh:
				return
			}
		}
	}()
}

func (w *WalValve) check() {
	growth := w.growthBytes()
	if growth <= w.softBytes {
		return
	}
	_ = w.vault.CheckpointWalPassive()
	w.mu.Lock()
	w.baseline = w.vault.GetWalSizeBytes()
	w.mu.Unlock()
}

func (w *WalValve) growthBytes() int64 {
	w.mu.Lock()
	baseline := w.baseline
	w.mu.Unlock()
	cur := w.vault.GetWalSizeBytes()
	if cur < baseline {
		return 0
	}
	return cur - baseline
}

// Backpressure pauses writers past the hard cap; call between transactions.
// Nil means no pressure.
func (w *WalValve) Backpressure() <-chan struct{} {
	growth := w.growthBytes()
	walSize := w.vault.GetWalSizeBytes()
	if growth <= w.hardBytes && walSize <= w.fileCapBytes {
		return nil
	}
	w.mu.Lock()
	if w.pauseCh != nil {
		ch := w.pauseCh
		w.mu.Unlock()
		return ch
	}
	ch := make(chan struct{})
	w.pauseCh = ch
	w.mu.Unlock()
	go func() {
		_ = w.vault.CheckpointWalPassive()
		w.mu.Lock()
		w.baseline = w.vault.GetWalSizeBytes()
		closeCh := w.pauseCh
		w.pauseCh = nil
		w.mu.Unlock()
		if closeCh != nil {
			close(closeCh)
		}
	}()
	return ch
}

// FoldNow runs one passive fold when growth is positive.
func (w *WalValve) FoldNow() error {
	w.mu.Lock()
	baseline := w.baseline
	w.mu.Unlock()
	cur := w.vault.GetWalSizeBytes()
	if cur <= baseline {
		return nil
	}
	return w.vault.CheckpointWalPassive()
}

func (w *WalValve) Stop() {
	w.mu.Lock()
	if w.ticker != nil {
		w.ticker.Stop()
		w.ticker = nil
	}
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
		w.stopCh = make(chan struct{})
	}
	ch := w.pauseCh
	w.pauseCh = nil
	w.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// Drain waits for any ongoing pause.
func (w *WalValve) Drain() {
	w.mu.Lock()
	ch := w.pauseCh
	w.mu.Unlock()
	if ch != nil {
		<-ch
	}
}
