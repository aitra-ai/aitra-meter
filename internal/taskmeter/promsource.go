package taskmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// PromSource resolves per-model efficiency from the meter's Prometheus:
// aitra_j_per_token (GPU-only) and aitra_system_j_per_token (GPU+host).
//
// Readings are polled on an interval and served from cache. A model whose
// gauge currently reads 0 (quiet window) keeps its last non-zero reading for
// up to Staleness: the task's own request is what ends the quiet spell, and
// billing it at the zeroed gauge would price real tokens at zero energy.
type PromSource struct {
	BaseURL   string        // e.g. http://aitra-meter-prometheus:9090
	Interval  time.Duration // poll cadence (default 10s)
	Staleness time.Duration // how long a last-known-good reading stays usable (default 5m)
	Client    *http.Client

	mu  sync.RWMutex
	eff map[string]Efficiency
}

// NewPromSource creates a PromSource and starts its poll loop.
func NewPromSource(ctx context.Context, baseURL string) *PromSource {
	s := &PromSource{
		BaseURL:   baseURL,
		Interval:  10 * time.Second,
		Staleness: 5 * time.Minute,
		Client:    &http.Client{Timeout: 8 * time.Second},
		eff:       make(map[string]Efficiency),
	}
	go s.loop(ctx)
	return s
}

// Efficiency implements EfficiencySource.
func (s *PromSource) Efficiency(model string) Efficiency {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.eff[model]
	if time.Since(e.At) > s.Staleness {
		return Efficiency{}
	}
	return e
}

func (s *PromSource) loop(ctx context.Context) {
	s.poll(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.poll(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *PromSource) poll(ctx context.Context) {
	now := time.Now()
	gpu, err := s.query(ctx, `aitra_j_per_token`)
	if err != nil {
		return // keep last-known-good; Staleness bounds how long
	}
	system, _ := s.query(ctx, `aitra_system_j_per_token`) // optional: absent when host unmeasured

	s.mu.Lock()
	defer s.mu.Unlock()
	for model, jpt := range gpu {
		if jpt <= 0 {
			continue // quiet window — keep last non-zero reading
		}
		e := Efficiency{JPT: jpt, At: now}
		if sys := system[model]; sys > 0 {
			e.SystemJPT = sys
		}
		s.eff[model] = e
	}
}

// query runs an instant query and returns model → max(value) across series.
// Max, not sum: the same model can appear under several label sets
// (hardware relabels, workload splits) that are the same physical series.
func (s *PromSource) query(ctx context.Context, q string) (map[string]float64, error) {
	u := s.BaseURL + "/api/v1/query?query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus query %q: HTTP %d", q, resp.StatusCode)
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(body.Data.Result))
	for _, r := range body.Data.Result {
		model := r.Metric["model"]
		if model == "" || len(r.Value) != 2 {
			continue
		}
		str, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			continue
		}
		if v > out[model] {
			out[model] = v
		}
	}
	return out, nil
}
