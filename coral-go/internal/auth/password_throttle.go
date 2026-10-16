package auth

import (
	"sync"
	"time"
)

// passwordThrottle slows wrong passwords without ever locking anyone out (#3949, Security 3 and
// 4): per source address, the first freeFails are free, then each attempt waits 2^(n-freeFails)
// seconds, capped at maxDelay; past globalLimit wrong passwords a minute from everyone, each
// attempt waits a second; and argon2 runs at most hashSlots at a time, one per address, an
// attempt waiting up to slotWait for a slot. Only password attempts count: the sign-in link, the
// CLI signature, the API key and an existing session never pass through here.
type passwordThrottle struct {
	mu       sync.Mutex
	now      func() time.Time
	sleep    func(time.Duration)
	perIP    map[string]*ipFails
	inflight map[string]bool
	global   []time.Time // wrong passwords in the last minute, oldest first
	slots    chan struct{}
}

type ipFails struct {
	n    int
	last time.Time
}

const (
	freeFails   = 5
	maxDelay    = 15 * time.Minute
	forgetAfter = 30 * time.Minute
	globalLimit = 60
	globalPause = time.Second
	hashSlots   = 2
	slotWait    = 2 * time.Second
	maxIPs      = 10000
)

func newPasswordThrottle() *passwordThrottle {
	return &passwordThrottle{
		now: time.Now, sleep: time.Sleep,
		perIP: map[string]*ipFails{}, inflight: map[string]bool{}, slots: make(chan struct{}, hashSlots),
	}
}

// delay is how long ip must still wait before its next attempt (0: it may try now).
func (t *passwordThrottle) delay(ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.perIP[ip]
	if f == nil {
		return 0
	}
	now := t.now()
	if now.Sub(f.last) >= forgetAfter {
		delete(t.perIP, ip)
		return 0
	}
	if f.n < freeFails {
		return 0
	}
	wait := maxDelay
	if shift := f.n - freeFails; shift < 20 {
		if d := time.Duration(1<<shift) * time.Second; d < maxDelay {
			wait = d
		}
	}
	if left := f.last.Add(wait).Sub(now); left > 0 {
		return left
	}
	return 0
}

// acquire takes a hash slot for ip: false at once if ip already has an attempt in flight, or
// after slotWait if every slot stays busy. release must be called after a true.
func (t *passwordThrottle) acquire(ip string) (release func(), ok bool) {
	t.mu.Lock()
	if t.inflight[ip] {
		t.mu.Unlock()
		return nil, false
	}
	t.inflight[ip] = true
	busy := t.globalBusyLocked()
	t.mu.Unlock()
	timer := time.NewTimer(slotWait)
	defer timer.Stop()
	select {
	case t.slots <- struct{}{}:
	case <-timer.C:
		t.mu.Lock()
		delete(t.inflight, ip)
		t.mu.Unlock()
		return nil, false
	}
	if busy {
		t.sleep(globalPause)
	}
	return func() {
		<-t.slots
		t.mu.Lock()
		delete(t.inflight, ip)
		t.mu.Unlock()
	}, true
}

// globalBusyLocked reports more than globalLimit wrong passwords in the last minute.
func (t *passwordThrottle) globalBusyLocked() bool {
	cut := t.now().Add(-time.Minute)
	i := 0
	for i < len(t.global) && t.global[i].Before(cut) {
		i++
	}
	t.global = t.global[i:]
	return len(t.global) > globalLimit
}

// fail counts a wrong password from ip.
func (t *passwordThrottle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.perIP) >= maxIPs {
		for k, f := range t.perIP {
			if now.Sub(f.last) >= forgetAfter {
				delete(t.perIP, k)
			}
		}
	}
	f := t.perIP[ip]
	if f == nil {
		if len(t.perIP) >= maxIPs {
			return // a flood of addresses: the global pause still applies
		}
		f = &ipFails{}
		t.perIP[ip] = f
	}
	f.n++
	f.last = now
	if len(t.global) < 4*globalLimit {
		t.global = append(t.global, now)
	}
}

// success clears ip's count.
func (t *passwordThrottle) success(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.perIP, ip)
}
