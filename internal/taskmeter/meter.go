// Package taskmeter accumulates per-task (agent-session) energy from
// per-request token usage joined against the meter's live per-model J/token.
//
// A "task" is one logical agent run — in the xModeling integration, one
// sandbox instance — identified by an opaque task ID that rides either in the
// request path (/t/{taskID}/v1/...) or the X-Aitra-Task-Id header. Energy is
// attributed by token share: each LLM call contributes
// output_tokens × J/token(model, now). This is proportional attribution over
// batched serving, not physical isolation — the same contract as the meter's
// host-energy split — and is labelled as such in every report.
package taskmeter

import (
	"sort"
	"sync"
	"time"
)

// joulesPerKWh converts J → kWh for the cost/carbon derivations.
const joulesPerKWh = 3_600_000.0

// Efficiency is one model's current J/token reading, resolved by the caller
// (the gateway polls Prometheus). SystemJPT is 0 when host energy is not
// measured; GPU-only J/token is then the only truthful number and system
// figures are omitted downstream — absent, never zero.
type Efficiency struct {
	JPT       float64 // GPU-only joules per output token
	SystemJPT float64 // (GPU+host) joules per output token; 0 = unmeasured
	At        time.Time
}

// EfficiencySource resolves the current efficiency for a model.
// Implementations may cache; a zero-value Efficiency means "unknown".
type EfficiencySource interface {
	Efficiency(model string) Efficiency
}

// Call is one recorded LLM request within a task.
type Call struct {
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	PromptTokens uint64    `json:"prompt_tokens"`
	OutputTokens uint64    `json:"output_tokens"`
	GPUJoules    float64   `json:"gpu_joules"`
	SystemJoules float64   `json:"system_joules,omitempty"` // 0 when host unmeasured
	JPTUsed      float64   `json:"j_per_token_used"`
}

// ModelUsage is a task's accumulated usage of one model.
type ModelUsage struct {
	Model        string  `json:"model"`
	Calls        uint64  `json:"calls"`
	PromptTokens uint64  `json:"prompt_tokens"`
	OutputTokens uint64  `json:"output_tokens"`
	GPUJoules    float64 `json:"gpu_joules"`
	SystemJoules float64 `json:"system_joules,omitempty"`
}

// Task is the accumulated bill for one agent task.
type Task struct {
	ID           string       `json:"id"`
	StartedAt    time.Time    `json:"started_at"`
	LastActivity time.Time    `json:"last_activity"`
	Calls        uint64       `json:"calls"`
	PromptTokens uint64       `json:"prompt_tokens"`
	OutputTokens uint64       `json:"output_tokens"`
	GPUJoules    float64      `json:"gpu_joules"`
	SystemJoules float64      `json:"system_joules,omitempty"`
	CostUSD      float64      `json:"cost_usd,omitempty"`
	CO2Grams     float64      `json:"co2_grams,omitempty"`
	Attribution  string       `json:"attribution_method"`
	ByModel      []ModelUsage `json:"by_model,omitempty"`
	RecentCalls  []Call       `json:"recent_calls,omitempty"`
}

// Meter accumulates task energy. Safe for concurrent use.
type Meter struct {
	src EfficiencySource

	// CostPerKWh and GCO2PerKWh derive the bill lines; zero disables the
	// derivation and the fields stay absent (site-unconfigured contract).
	CostPerKWh float64
	GCO2PerKWh float64

	// MaxRecentCalls bounds the per-task call log (default 50).
	MaxRecentCalls int

	mu    sync.Mutex
	tasks map[string]*taskState
}

type taskState struct {
	Task
	byModel map[string]*ModelUsage
}

// New creates a Meter reading efficiencies from src.
func New(src EfficiencySource) *Meter {
	return &Meter{src: src, MaxRecentCalls: 50, tasks: make(map[string]*taskState)}
}

// Record books one LLM call against a task and returns the call's bill.
// A model with no known efficiency books tokens but zero joules — the tokens
// are real and the energy is unknown; recording zero energy for an unknown
// model would be wrong, so JPTUsed=0 marks the call as unmetered.
func (m *Meter) Record(taskID, model string, promptTokens, outputTokens uint64) Call {
	now := time.Now()
	eff := m.src.Efficiency(model)

	c := Call{
		At:           now,
		Model:        model,
		PromptTokens: promptTokens,
		OutputTokens: outputTokens,
		JPTUsed:      eff.JPT,
	}
	c.GPUJoules = float64(outputTokens) * eff.JPT
	if eff.SystemJPT > 0 {
		c.SystemJoules = float64(outputTokens) * eff.SystemJPT
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.tasks[taskID]
	if !ok {
		st = &taskState{
			Task:    Task{ID: taskID, StartedAt: now, Attribution: "token-share"},
			byModel: make(map[string]*ModelUsage),
		}
		m.tasks[taskID] = st
	}
	st.LastActivity = now
	st.Calls++
	st.PromptTokens += promptTokens
	st.OutputTokens += outputTokens
	st.GPUJoules += c.GPUJoules
	st.SystemJoules += c.SystemJoules

	mu, ok := st.byModel[model]
	if !ok {
		mu = &ModelUsage{Model: model}
		st.byModel[model] = mu
	}
	mu.Calls++
	mu.PromptTokens += promptTokens
	mu.OutputTokens += outputTokens
	mu.GPUJoules += c.GPUJoules
	mu.SystemJoules += c.SystemJoules

	st.RecentCalls = append(st.RecentCalls, c)
	if max := m.MaxRecentCalls; max > 0 && len(st.RecentCalls) > max {
		st.RecentCalls = st.RecentCalls[len(st.RecentCalls)-max:]
	}
	return c
}

// billed fills the derived cost/carbon lines on a copied Task.
// Bill from system joules when measured, else GPU-only — never zero-pad.
func (m *Meter) billed(t Task) Task {
	j := t.SystemJoules
	if j == 0 {
		j = t.GPUJoules
	}
	if m.CostPerKWh > 0 {
		t.CostUSD = j / joulesPerKWh * m.CostPerKWh
	}
	if m.GCO2PerKWh > 0 {
		t.CO2Grams = j / joulesPerKWh * m.GCO2PerKWh
	}
	return t
}

// Get returns one task's bill (detail view), or ok=false.
func (m *Meter) Get(taskID string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.tasks[taskID]
	if !ok {
		return Task{}, false
	}
	t := st.Task
	t.ByModel = make([]ModelUsage, 0, len(st.byModel))
	for _, mu := range st.byModel {
		t.ByModel = append(t.ByModel, *mu)
	}
	sort.Slice(t.ByModel, func(i, j int) bool { return t.ByModel[i].GPUJoules > t.ByModel[j].GPUJoules })
	t.RecentCalls = append([]Call(nil), st.RecentCalls...)
	return m.billed(t), true
}

// List returns task summaries (no per-call log), most recently active first.
func (m *Meter) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.tasks))
	for _, st := range m.tasks {
		t := st.Task
		t.RecentCalls = nil
		t.ByModel = nil
		out = append(out, m.billed(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity.After(out[j].LastActivity) })
	return out
}
