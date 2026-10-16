package taskmeter

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// CPUStartRequest is the optional JSON payload for POST /tasks/{id}/cpu/start.
type CPUStartRequest struct {
	PID        int    `json:"pid,omitempty"`
	CgroupPath string `json:"cgroup_path,omitempty"`
}

// CPUHandler exposes HTTP endpoints for controlling HostTracker per-task CPU measurement.
type CPUHandler struct {
	Tracker *HostTracker
	Meter   *Meter
	Log     *zap.Logger
}

// NewCPUHandler creates a new CPUHandler.
func NewCPUHandler(tracker *HostTracker, meter *Meter, log *zap.Logger) *CPUHandler {
	if log == nil {
		log = zap.NewNop()
	}
	return &CPUHandler{
		Tracker: tracker,
		Meter:   meter,
		Log:     log,
	}
}

// HandleStart handles POST /tasks/{id}/cpu/start.
func (h *CPUHandler) HandleStart(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.Tracker == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "host CPU telemetry provider not configured or unavailable",
		})
		return
	}

	var req CPUStartRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Also check X-Aitra-Container-Cgroup header if not provided in JSON body
	if req.CgroupPath == "" {
		req.CgroupPath = r.Header.Get("X-Aitra-Container-Cgroup")
	}

	target := AttributionTarget(req)

	if err := h.Tracker.StartTarget(taskID, target); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	resp := map[string]any{
		"status":  "ok",
		"task_id": taskID,
	}
	if req.PID > 0 {
		resp["pid"] = req.PID
	}
	if req.CgroupPath != "" {
		resp["cgroup_path"] = req.CgroupPath
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleStop handles POST /tasks/{id}/cpu/stop.
func (h *CPUHandler) HandleStop(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.Tracker == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "host CPU telemetry provider not configured or unavailable",
		})
		return
	}

	meas, err := h.Tracker.Stop(taskID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
		} else if errors.Is(err, ErrCounterDiscontinuity) {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	if h.Meter != nil && meas.AttributedJoules > 0 {
		h.Meter.AddHostEnergy(taskID, meas.AttributedJoules)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(meas)
}
