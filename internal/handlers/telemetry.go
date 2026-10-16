package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/orbit/control-server/internal/repository"
)


type TelemetryHandler struct {
	db        *repository.DB
	startTime time.Time
}

func NewTelemetryHandler(db *repository.DB) *TelemetryHandler {
	return &TelemetryHandler{
		db:        db,
		startTime: time.Now(),
	}
}

type MemoryStats struct {
	AllocMb     float64 `json:"allocMb"`
	SysMb       float64 `json:"sysMb"`
	HeapAllocMb float64 `json:"heapAllocMb"`
}

type TelemetryResponse struct {
	Status        string                    `json:"status"`
	UptimeSeconds int64                     `json:"uptimeSeconds"`
	GoVersion     string                    `json:"goVersion"`
	Goroutines    int                       `json:"goroutines"`
	Memory        MemoryStats               `json:"memory"`
	Database      repository.TelemetryStats `json:"database"`
	Storage       struct {
		DeltaBlobsCount    int   `json:"deltaBlobsCount"`
		DeltaSizeBytes     int64 `json:"deltaSizeBytes"`
		WebRTCSignalsCount int   `json:"webrtcSignalsCount"`
	} `json:"storage"`
	Timestamp string `json:"timestamp"`
}

// GetStatus returns instant, sub-millisecond server health and telemetry.
func (h *TelemetryHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	dbStats := h.db.GetTelemetryStats()

	resp := TelemetryResponse{
		Status:        "ONLINE",
		UptimeSeconds: int64(time.Since(h.startTime).Seconds()),
		GoVersion:     runtime.Version(),
		Goroutines:    runtime.NumGoroutine(),
		Memory: MemoryStats{
			AllocMb:     float64(m.Alloc) / (1024 * 1024),
			SysMb:       float64(m.Sys) / (1024 * 1024),
			HeapAllocMb: float64(m.HeapAlloc) / (1024 * 1024),
		},
		Database:  dbStats,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	resp.Storage.DeltaBlobsCount = dbStats.DeltaBlobsCount
	resp.Storage.DeltaSizeBytes = dbStats.DeltaSizeBytes
	resp.Storage.WebRTCSignalsCount = dbStats.WebRTCSignalsCount

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// TriggerSweep runs maintenance sweeps for cleanup.
func (h *TelemetryHandler) TriggerSweep(w http.ResponseWriter, r *http.Request) {
	h.db.HeartbeatSweep()
	h.db.SweepExpiredSignals(30 * time.Minute)
	h.db.MessageSweep()
	h.db.ActivityLogSweep()
	deltasSwept := h.db.SweepExpiredDeltas(0) // Full Zero-Storage Delta sweep
	orphansSwept := h.db.SweepOrphanedProjects(24 * time.Hour)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "success",
		"message":        "Maintenance sweeps completed",
		"deltas_swept":   deltasSwept,
		"orphans_swept":  orphansSwept,
	})
}

// PurgeData verifies the control server secret header and purges users and projects data.
func (h *TelemetryHandler) PurgeData(w http.ResponseWriter, r *http.Request) {
	// 1. Verify Secret Header
	expectedSecret := os.Getenv("CONTROL_SERVER_SECRET")
	if expectedSecret == "" {
		expectedSecret = "orbit-control-server-verification-secret-2026"
	}
	if r.Header.Get("X-Control-Server-Secret") != expectedSecret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized control server secret"})
		return
	}

	// 2. Clear Users and Projects Data
	if err := h.db.PurgeData(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to purge control server data: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Control Server users and projects purged successfully.",
	})
}

