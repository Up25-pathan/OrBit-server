package repository

import (
	"log"
	"time"
)

func (db *DB) SweepExpiredSignals(ttl time.Duration) int {
	if db.pg != nil {
		return db.pg.sweepExpiredSignals(ttl)
	}
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
	if db.pg != nil {
		return db.pg.saveSignal(projectID, fromPeer, toPeer, signalType, payload)
	}
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

	if err := db.save(); err != nil {
		return err
	}
	return nil
}

func (db *DB) GetPendingSignalsForPeer(projectID, toPeer string) ([]Signal, error) {
	if db.pg != nil {
		return db.pg.getPendingSignalsForPeer(projectID, toPeer)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	var result []Signal
	var kept []Signal
	for _, s := range db.data.Signals {
		if s.ProjectID == projectID && s.ToPeer == toPeer {
			result = append(result, s)
		} else {
			kept = append(kept, s)
		}
	}
	db.data.Signals = kept
	if err := db.saveUnderLock(); err != nil {
		return nil, err
	}
	return result, nil
}

func (db *DB) ClearSignalsForPeer(projectID, toPeer string) error {
	if db.pg != nil {
		return db.pg.clearSignalsForPeer(projectID, toPeer)
	}
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
