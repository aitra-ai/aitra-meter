// multiloop.go implements per-model attribution for multi-model GPU nodes.
//
// MultiLoop is the per-model variant of Loop. Instead of reading one fixed
// inference endpoint, it discovers every GPU-holding pod on its node each
// window and attributes energy to each pod by the exact GPUs it holds:
//
//	kubelet device-plugin checkpoint → pod UID → GPU UUIDs
//	pod list (this node)             → pod UID → IP, port, labels
//	per-device energy (dcgm)         → GPU UUID → cumulative joules
//	per-pod vLLM /metrics            → cumulative output tokens, model name
//
// Each window it sends one WindowReport per model pod — energy is the delta
// of exactly that pod's GPUs — plus one residual report
// (model.ResidualModelName) carrying the unallocated GPUs' energy, which the
// aggregation service records as true idle power. Pods appear and disappear
// without configuration: whatever holds a GPU next window gets measured. This
// is what makes platform-launched models (csghub, KServe, plain Deployments)
// show up on the dashboard automatically.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/provider"

	// MultiLoop scrapes every discovered pod with the vllm provider by name;
	// register it here rather than relying on the binary's import list.
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/vllm"
)

const (
	// DefaultCheckpointPath is where kubelet keeps device-plugin allocations.
	DefaultCheckpointPath = "/var/lib/kubelet/device-plugins/kubelet_internal_checkpoint"

	gpuResourceName      = "nvidia.com/gpu"
	defaultInferencePort = 8000
)

// MultiConfig holds parameters for MultiLoop.
type MultiConfig struct {
	// Node is the Kubernetes node name; only pods on this node are measured.
	Node string

	// AggregatorAddr is the gRPC address of the aggregation service.
	AggregatorAddr string

	// WindowDuration controls how often WindowReports are sent.
	WindowDuration time.Duration

	// Energy is the provider used for names/logging; PerDevice must expose the
	// same provider's per-device counters (the dcgm provider implements both).
	Energy    provider.EnergyProvider
	PerDevice provider.PerDeviceEnergy

	// K8s lists pods on Node to resolve which pod holds which GPU.
	K8s kubernetes.Interface

	// CheckpointPath is the kubelet device-plugin checkpoint file.
	// Defaults to DefaultCheckpointPath.
	CheckpointPath string

	// InferencePort is used when a GPU pod declares no container port.
	// Defaults to 8000 (vLLM).
	InferencePort int
}

// podState tracks the per-pod scrape state across windows.
type podState struct {
	prov       provider.InferenceMetricsProvider
	prevTokens uint64
	seenTokens bool
	modelName  string
}

// gpuPod is a Running pod on this node that holds at least one GPU.
type gpuPod struct {
	uid       string
	name      string
	namespace string
	ip        string
	port      int
	fallback  string // model-name fallback from labels when /metrics is unreadable
}

// MultiLoop runs the per-model measurement loop for one node.
type MultiLoop struct {
	cfg    MultiConfig
	log    *zap.Logger
	client measurementv1.MeasurementServiceClient
	conn   *grpc.ClientConn

	pods       map[string]*podState // pod UID → scrape state
	prevEnergy map[string]float64   // GPU UUID → cumulative joules at last tick
	prevTime   time.Time
	windowSeq  uint64
}

// NewMultiLoop creates a MultiLoop and dials the aggregation service.
func NewMultiLoop(cfg MultiConfig, log *zap.Logger) (*MultiLoop, error) {
	if cfg.Node == "" {
		return nil, fmt.Errorf("agent.MultiConfig.Node is required")
	}
	if cfg.AggregatorAddr == "" {
		return nil, fmt.Errorf("agent.MultiConfig.AggregatorAddr is required")
	}
	if cfg.PerDevice == nil {
		return nil, fmt.Errorf("agent.MultiConfig.PerDevice is required")
	}
	if cfg.K8s == nil {
		return nil, fmt.Errorf("agent.MultiConfig.K8s is required")
	}
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = DefaultWindowDuration
	}
	if cfg.CheckpointPath == "" {
		cfg.CheckpointPath = DefaultCheckpointPath
	}
	if cfg.InferencePort <= 0 {
		cfg.InferencePort = defaultInferencePort
	}

	conn, err := grpc.NewClient(
		cfg.AggregatorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial aggregation service %q: %w", cfg.AggregatorAddr, err)
	}

	return &MultiLoop{
		cfg:    cfg,
		log:    log,
		client: measurementv1.NewMeasurementServiceClient(conn),
		conn:   conn,
		pods:   make(map[string]*podState),
	}, nil
}

// Close releases the gRPC connection.
func (m *MultiLoop) Close() error { return m.conn.Close() }

// Run blocks until ctx is cancelled. The first tick only records baselines;
// reports start from the second tick.
func (m *MultiLoop) Run(ctx context.Context) {
	m.log.Info("per-model measurement loop started",
		zap.String("node", m.cfg.Node),
		zap.String("energy_provider", m.cfg.Energy.Name()),
		zap.String("checkpoint", m.cfg.CheckpointPath),
		zap.Duration("window", m.cfg.WindowDuration),
	)
	m.tick(ctx)

	ticker := time.NewTicker(m.cfg.WindowDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.tick(ctx)
		case <-ctx.Done():
			m.log.Info("per-model measurement loop stopped")
			return
		}
	}
}

// tick snapshots per-device energy, discovers GPU pods, and (from the second
// tick on) reports the elapsed window.
func (m *MultiLoop) tick(ctx context.Context) {
	now := time.Now()

	energy, err := m.cfg.PerDevice.DeviceEnergyJoules(ctx)
	if err != nil {
		// Keep the previous snapshot: the next successful tick spans the gap,
		// so no energy is lost — the window is just longer.
		m.log.Warn("per-device energy read failed — skipping tick", zap.Error(err))
		return
	}

	alloc, err := readCheckpoint(m.cfg.CheckpointPath)
	if err != nil {
		m.log.Warn("kubelet checkpoint read failed — treating all GPUs as unallocated",
			zap.String("path", m.cfg.CheckpointPath), zap.Error(err))
		alloc = nil
	}

	pods, err := m.gpuPods(ctx, alloc)
	if err != nil {
		m.log.Warn("GPU pod discovery failed — treating all GPUs as unallocated", zap.Error(err))
		pods = nil
	}

	if !m.prevTime.IsZero() {
		m.report(ctx, now, energy, alloc, pods)
	}

	m.prevEnergy = energy
	m.prevTime = now
	m.prune(pods)
}

// report sends one WindowReport per model pod plus the residual report.
func (m *MultiLoop) report(ctx context.Context, now time.Time, energy map[string]float64, alloc map[string][]string, pods []gpuPod) {
	elapsed := now.Sub(m.prevTime).Seconds()
	if elapsed <= 0 {
		return
	}

	uids := make([]string, len(pods))
	for i := range pods {
		uids[i] = pods[i].uid
	}
	perPod, residual := attributeEnergy(m.prevEnergy, energy, alloc, uids)

	tsMs := now.UnixMilli()
	for _, p := range pods {
		st := m.state(p)
		tokens := m.tokenDelta(ctx, st, p)
		name := m.resolveModelName(ctx, st, p)
		joules := perPod[p.uid]
		m.send(ctx, &measurementv1.WindowReport{
			WindowId:          m.nextWindowID(name),
			Node:              m.cfg.Node,
			ModelName:         name,
			EnergyJoules:      joules,
			OutputTokens:      tokens,
			PowerWatts:        joules / elapsed,
			EnergyProvider:    m.cfg.Energy.Name(),
			InferenceProvider: "vllm",
			TimestampUnixMs:   tsMs,
		})
	}

	// Residual: energy of GPUs no pod holds. With no model pods at all this
	// degrades to the classic whole-node idle report (empty model name).
	residualName := model.ResidualModelName
	if len(pods) == 0 {
		residualName = ""
	}
	m.send(ctx, &measurementv1.WindowReport{
		WindowId:          m.nextWindowID("idle"),
		Node:              m.cfg.Node,
		ModelName:         residualName,
		EnergyJoules:      residual,
		OutputTokens:      0,
		PowerWatts:        residual / elapsed,
		EnergyProvider:    m.cfg.Energy.Name(),
		InferenceProvider: "vllm",
		TimestampUnixMs:   tsMs,
	})
}

// state returns (creating if needed) the scrape state for a pod.
func (m *MultiLoop) state(p gpuPod) *podState {
	st, ok := m.pods[p.uid]
	if ok {
		return st
	}
	endpoint := fmt.Sprintf("http://%s:%d/metrics", p.ip, p.port)
	prov, err := provider.NewInference("vllm", map[string]string{"endpoint": endpoint})
	if err != nil {
		// The vllm factory cannot fail today; guard against future factories.
		m.log.Warn("inference provider init failed",
			zap.String("pod", p.namespace+"/"+p.name), zap.Error(err))
		prov = nil
	} else {
		m.log.Info("discovered GPU pod",
			zap.String("pod", p.namespace+"/"+p.name),
			zap.String("endpoint", endpoint))
	}
	st = &podState{prov: prov}
	m.pods[p.uid] = st
	return st
}

// tokenDelta reads the pod's cumulative token counter and returns the delta
// since the previous window. The first read only records a baseline. A failed
// read (vLLM still loading, or a non-vLLM GPU workload) reports zero tokens —
// the pod's energy is still attributed to it.
func (m *MultiLoop) tokenDelta(ctx context.Context, st *podState, p gpuPod) uint64 {
	if st.prov == nil {
		return 0
	}
	curr, err := st.prov.OutputTokens(ctx)
	if err != nil {
		m.log.Debug("token read failed (pod loading or non-vLLM)",
			zap.String("pod", p.namespace+"/"+p.name), zap.Error(err))
		return 0
	}
	var delta uint64
	if st.seenTokens && curr >= st.prevTokens {
		delta = curr - st.prevTokens
	}
	st.prevTokens = curr
	st.seenTokens = true
	return delta
}

// resolveModelName prefers the served model name from the pod's own /metrics
// (--served-model-name), falling back to pod labels, then the pod name.
func (m *MultiLoop) resolveModelName(ctx context.Context, st *podState, p gpuPod) string {
	if st.modelName != "" {
		return st.modelName
	}
	if st.prov != nil {
		if name, err := st.prov.ModelName(ctx); err == nil && name != "" && name != "unknown" {
			st.modelName = name
			return name
		}
	}
	return p.fallback
}

// gpuPods lists Running pods on this node that appear in the checkpoint.
func (m *MultiLoop) gpuPods(ctx context.Context, alloc map[string][]string) ([]gpuPod, error) {
	if len(alloc) == 0 {
		return nil, nil
	}
	list, err := m.cfg.K8s.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + m.cfg.Node,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods on %s: %w", m.cfg.Node, err)
	}
	var out []gpuPod
	for i := range list.Items {
		pod := &list.Items[i]
		if _, holds := alloc[string(pod.UID)]; !holds {
			continue
		}
		// The checkpoint keeps entries for terminated pods; only Running pods
		// with an IP are measurable.
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		out = append(out, gpuPod{
			uid:       string(pod.UID),
			name:      pod.Name,
			namespace: pod.Namespace,
			ip:        pod.Status.PodIP,
			port:      podPort(pod, m.cfg.InferencePort),
			fallback:  fallbackModelName(pod),
		})
	}
	return out, nil
}

// prune drops scrape state for pods that no longer hold GPUs.
func (m *MultiLoop) prune(pods []gpuPod) {
	alive := make(map[string]bool, len(pods))
	for _, p := range pods {
		alive[p.uid] = true
	}
	for uid := range m.pods {
		if !alive[uid] {
			delete(m.pods, uid)
		}
	}
}

func (m *MultiLoop) send(ctx context.Context, report *measurementv1.WindowReport) {
	ack, err := m.client.ReportWindow(ctx, report)
	if err != nil {
		m.log.Error("ReportWindow RPC failed",
			zap.String("window_id", report.WindowId), zap.Error(err))
		return
	}
	m.log.Debug("window reported",
		zap.String("model", report.ModelName),
		zap.Float64("joules", report.EnergyJoules),
		zap.Uint64("tokens", report.OutputTokens),
		zap.Bool("accepted", ack.Accepted),
	)
}

func (m *MultiLoop) nextWindowID(name string) string {
	m.windowSeq++
	return fmt.Sprintf("%s/permodel/%d/%s", m.cfg.Node, m.windowSeq, name)
}

// attributeEnergy splits the energy consumed between two per-device snapshots
// among pods by the devices each holds, returning joules per pod UID and the
// residual joules of devices no listed pod holds. Devices missing from either
// snapshot and negative deltas (counter resets) contribute zero.
func attributeEnergy(prev, curr map[string]float64, alloc map[string][]string, podUIDs []string) (map[string]float64, float64) {
	perPod := make(map[string]float64, len(podUIDs))
	allocated := map[string]bool{}
	for _, uid := range podUIDs {
		var joules float64
		for _, dev := range alloc[uid] {
			allocated[dev] = true
			p, okP := prev[dev]
			c, okC := curr[dev]
			if okP && okC && c > p {
				joules += c - p
			}
		}
		perPod[uid] = joules
	}
	var residual float64
	for dev, c := range curr {
		if allocated[dev] {
			continue
		}
		if p, ok := prev[dev]; ok && c > p {
			residual += c - p
		}
	}
	return perPod, residual
}

// podPort returns the first declared container port, or def when none is set.
func podPort(pod *corev1.Pod, def int) int {
	for i := range pod.Spec.Containers {
		for _, p := range pod.Spec.Containers[i].Ports {
			if p.ContainerPort > 0 {
				return int(p.ContainerPort)
			}
		}
	}
	return def
}

// fallbackModelName picks a model name for pods whose inference endpoint is
// not readable (yet): explicit aitra label, common platform labels, pod name.
func fallbackModelName(pod *corev1.Pod) string {
	for _, key := range []string{"aitra-ai.github.io/model-name", "model", "app"} {
		if v := pod.Labels[key]; v != "" {
			return v
		}
	}
	return pod.Name
}

// kubeletCheckpoint mirrors the kubelet device-plugin checkpoint layout. Only
// the fields needed for pod→device mapping are declared.
type kubeletCheckpoint struct {
	Data struct {
		PodDeviceEntries []struct {
			PodUID        string          `json:"PodUID"`
			ContainerName string          `json:"ContainerName"`
			ResourceName  string          `json:"ResourceName"`
			DeviceIDs     json.RawMessage `json:"DeviceIDs"`
		} `json:"PodDeviceEntries"`
	} `json:"Data"`
}

// readCheckpoint parses the kubelet device-plugin checkpoint into
// pod UID → GPU device IDs (UUIDs). DeviceIDs is a NUMA-node map on current
// kubelets and a flat array on older ones; both are accepted. Entries for
// non-GPU resources are ignored. The checkpoint can retain terminated pods, so
// callers must intersect with currently Running pods.
func readCheckpoint(path string) (map[string][]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cp kubeletCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string][]string{}
	for _, e := range cp.Data.PodDeviceEntries {
		if e.ResourceName != gpuResourceName {
			continue
		}
		var byNUMA map[string][]string
		if err := json.Unmarshal(e.DeviceIDs, &byNUMA); err == nil {
			for _, ids := range byNUMA {
				out[e.PodUID] = append(out[e.PodUID], ids...)
			}
			continue
		}
		var flat []string
		if err := json.Unmarshal(e.DeviceIDs, &flat); err == nil {
			out[e.PodUID] = append(out[e.PodUID], flat...)
		}
	}
	return out, nil
}
