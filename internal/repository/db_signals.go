package repository

import (
	"log"
	"time"
)

func (db *DB) SweepExpiredSignals(ttl time.Duration) int {
	db.mu.Lock()
	cutoff := time.Now().UTC().Add(-ttl)
	var kept []Signal
	for _, s := range db.data.Signals {
		if s.CreatedAt.After(cutoff) {
			kept = append(kept, s)
		}
	}
	swept := len(db.data.Signals) - len(kept)
	db.data.Signals = kept
	db.mu.Unlock()

	if swept > 0 {
		if err := db.save(); err != nil {
			log.Printf("[signal-sweep] save failed: %v", err)
		}
	}
	return swept
}

func (db *DB) SaveSignal(projectID, fromPeer, toPeer, signalType, payload string) error {
	db.mu.Lock()
	sig := Signal{
		ID:        generateID("sig"),
		ProjectID: projectID,
		FromPeer:  fromPeer,
		ToPeer:    toPeer,
		Type:      signalType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}

	cutoff := time.Now().UTC().Add(-30 * time.Minute)
	var kept []Signal
	for _, s := range db.data.Signals {
		if s.CreatedAt.IsZero() || s.CreatedAt.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, sig)
	db.data.Signals = kept
	db.mu.Unlock()

	return db.save()
}

func (db *DB) GetPendingSignalsForPeer(projectID, toPeer string) ([]Signal, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var result []Signal
	for _, s := range db.data.Signals {
		if s.ProjectID == projectID && s.ToPeer == toPeer {
			result = append(result, s)
		}
	}
	return result, nil
}

func (db *DB) ClearSignalsForPeer(projectID, toPeer string) error {
	db.mu.Lock()
	var kept []Signal
	for _, s := range db.data.Signals {
		if !(s.ProjectID == projectID && s.ToPeer == toPeer) {
			kept = append(kept, s)
		}
	}
	db.data.Signals = kept
	db.mu.Unlock()
	return db.save()
}
