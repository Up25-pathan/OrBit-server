package repository

import "time"

func (db *DB) SaveSignal(projectID, fromPeer, toPeer, signalType, payload string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	sig := Signal{
		ID:        generateID("sig"),
		ProjectID: projectID,
		FromPeer:  fromPeer,
		ToPeer:    toPeer,
		Type:      signalType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}

	// Sweep abandoned signals older than 5 minutes to prevent memory leaks
	cutoff := time.Now().UTC().Add(-5 * time.Minute)
	var kept []Signal
	for _, s := range db.data.Signals {
		if s.CreatedAt.IsZero() || s.CreatedAt.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, sig)
	db.data.Signals = kept

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
	defer db.mu.Unlock()
	var kept []Signal
	for _, s := range db.data.Signals {
		if !(s.ProjectID == projectID && s.ToPeer == toPeer) {
			kept = append(kept, s)
		}
	}
	db.data.Signals = kept
	return db.save()
}
