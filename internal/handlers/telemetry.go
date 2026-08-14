package handlers

import (
	"encoding/json"
	"net/http"
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Maintenance sweeps completed",
	})
}
