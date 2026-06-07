package sniper

import "time"

// CooldownTracker handles cooldown time validation.
// When an exit/close order fails, a 1-second cooldown is enforced to prevent
// duplicate order blasts and wait for broker state/portfolio synchronization.
type CooldownTracker struct {
	lastCloseErrorAt map[string]time.Time
}

func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		lastCloseErrorAt: make(map[string]time.Time),
	}
}

func (ct *CooldownTracker) Trigger(sniperID string) {
	ct.lastCloseErrorAt[sniperID] = time.Now()
}

func (ct *CooldownTracker) TriggerWithTime(sniperID string, t time.Time) {
	ct.lastCloseErrorAt[sniperID] = t
}

func (ct *CooldownTracker) IsCoolingDown(sniperID string, now time.Time) bool {
	lastErrTime, exists := ct.lastCloseErrorAt[sniperID]
	if !exists {
		return false
	}
	return now.Sub(lastErrTime) < 1*time.Second
}
