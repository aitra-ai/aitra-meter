package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/model"
)

// realistically shaped kubelet checkpoint: one NUMA-map GPU entry, one
// flat-array GPU entry (older kubelets), and one non-GPU resource entry.
const checkpointJSON = `{
  "Data": {
    "PodDeviceEntries": [
      {
        "PodUID": "pod-a",
        "ContainerName": "vllm",
        "ResourceName": "nvidia.com/gpu",
        "DeviceIDs": {"1": ["GPU-aaa", "GPU-bbb"]}
      },
      {
        "PodUID": "pod-b",
        "ContainerName": "vllm",
        "ResourceName": "nvidia.com/gpu",
        "DeviceIDs": ["GPU-ccc"]
      },
      {
        "PodUID": "pod-c",
        "ContainerName": "app",
        "ResourceName": "example.com/other",
        "DeviceIDs": {"0": ["OTHER-1"]}
      }
    ],
    "RegisteredDevices": {"nvidia.com/gpu": ["GPU-aaa", "GPU-bbb", "GPU-ccc"]}
  },
  "Checksum": 12345
}`

func TestReadCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubelet_internal_checkpoint")
	if err := os.WriteFile(path, []byte(checkpointJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	alloc, err := readCheckpoint(path)
	if err != nil {
		t.Fatalf("readCheckpoint: %v", err)
	}
	if len(alloc) != 2 {
		t.Fatalf("want 2 GPU pods, got %d: %v", len(alloc), alloc)
	}

	a := alloc["pod-a"]
	sort.Strings(a)
	if len(a) != 2 || a[0] != "GPU-aaa" || a[1] != "GPU-bbb" {
		t.Errorf("pod-a devices = %v, want [GPU-aaa GPU-bbb]", a)
	}
	if b := alloc["pod-b"]; len(b) != 1 || b[0] != "GPU-ccc" {
		t.Errorf("pod-b devices = %v, want [GPU-ccc] (flat-array form)", b)
	}
	if _, ok := alloc["pod-c"]; ok {
		t.Error("non-GPU resource entry must be ignored")
	}
}

func TestReadCheckpointMissingFile(t *testing.T) {
	if _, err := readCheckpoint(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for missing checkpoint file")
	}
}

func TestAttributeEnergy(t *testing.T) {
	prev := map[string]float64{
		"GPU-aaa": 1000, "GPU-bbb": 2000, // pod-a (two GPUs)
		"GPU-ccc": 3000, // pod-b, counter will reset
		"GPU-ddd": 4000, // unallocated → residual
		"GPU-eee": 5000, // vanishes from curr → ignored
	}
	curr := map[string]float64{
		"GPU-aaa": 1150, "GPU-bbb": 2250, // pod-a: 150 + 250 = 400 J
		"GPU-ccc": 10,   // reset: clamped to 0
		"GPU-ddd": 4600, // residual: 600 J
		"GPU-fff": 70,   // new device, no baseline → ignored
	}
	alloc := map[string][]string{
		"pod-a": {"GPU-aaa", "GPU-bbb"},
		"pod-b": {"GPU-ccc"},
		"pod-x": {"GPU-zzz"}, // terminated pod in checkpoint, not running
	}

	perPod, residual := attributeEnergy(prev, curr, alloc, []string{"pod-a", "pod-b"})

	if got := perPod["pod-a"]; got != 400 {
		t.Errorf("pod-a joules = %v, want 400", got)
	}
	if got := perPod["pod-b"]; got != 0 {
		t.Errorf("pod-b joules = %v, want 0 (counter reset clamps)", got)
	}
	if residual != 600 {
		t.Errorf("residual = %v, want 600 (GPU-ddd only)", residual)
	}
	if _, ok := perPod["pod-x"]; ok {
		t.Error("pods not in podUIDs must not appear in perPod")
	}
}

func TestAttributeEnergyNoPods(t *testing.T) {
	prev := map[string]float64{"GPU-aaa": 100, "GPU-bbb": 200}
	curr := map[string]float64{"GPU-aaa": 160, "GPU-bbb": 290}

	perPod, residual := attributeEnergy(prev, curr, nil, nil)
	if len(perPod) != 0 {
		t.Errorf("perPod = %v, want empty", perPod)
	}
	if residual != 150 {
		t.Errorf("residual = %v, want 150 (all GPUs unallocated)", residual)
	}
}

// --- end-to-end: discover → attribute → report ------------------------------

// fakePerDevice serves a scripted sequence of per-device energy snapshots.
// The last snapshot repeats once the script is exhausted.
type fakePerDevice struct {
	fakeEnergy
	mu    sync.Mutex
	snaps []map[string]float64
}

func (f *fakePerDevice) DeviceEnergyJoules(_ context.Context) (map[string]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snaps) == 0 {
		return nil, fmt.Errorf("no snapshot scripted")
	}
	s := f.snaps[0]
	if len(f.snaps) > 1 {
		f.snaps = f.snaps[1:]
	}
	return s, nil
}

// fakeVLLM serves a minimal vLLM /metrics whose token counter is controlled
// by the test through tokens. Returns the server and its listen port.
func fakeVLLM(t *testing.T, modelName string, tokens *atomic.Uint64) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "vllm:generation_tokens_total{model_name=%q} %d\n", modelName, tokens.Load())
		fmt.Fprintf(w, "vllm:num_requests_running 1\n")
	}))
	t.Cleanup(srv.Close)
	return srv, srv.Listener.Addr().(*net.TCPAddr).Port
}

func runningGPUPod(uid, name string, port int) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      name,
			Namespace: "demo",
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
			Containers: []corev1.Container{{
				Name:  "vllm",
				Ports: []corev1.ContainerPort{{ContainerPort: int32(port)}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "127.0.0.1"},
	}
}

// TestMultiLoopEndToEnd walks the full per-model path on a fake 4-GPU node:
// kubelet checkpoint → pod discovery → per-pod vLLM scrape → per-device
// energy attribution → one WindowReport per model plus the residual report.
// pod-a holds two GPUs (the TP=2 case), pod-b one, one GPU is unallocated.
func TestMultiLoopEndToEnd(t *testing.T) {
	var tokensA, tokensB atomic.Uint64
	tokensA.Store(1000)
	tokensB.Store(5000)
	_, portA := fakeVLLM(t, "model-a", &tokensA)
	_, portB := fakeVLLM(t, "model-b", &tokensB)

	cpPath := filepath.Join(t.TempDir(), "kubelet_internal_checkpoint")
	cp := fmt.Sprintf(`{"Data":{"PodDeviceEntries":[
		{"PodUID":"pod-a","ContainerName":"vllm","ResourceName":"nvidia.com/gpu","DeviceIDs":{"0":["GPU-aaa","GPU-bbb"]}},
		{"PodUID":"pod-b","ContainerName":"vllm","ResourceName":"nvidia.com/gpu","DeviceIDs":["GPU-ccc"]}
	]}}`)
	if err := os.WriteFile(cpPath, []byte(cp), 0o600); err != nil {
		t.Fatal(err)
	}

	perDevice := &fakePerDevice{snaps: []map[string]float64{
		{"GPU-aaa": 1000, "GPU-bbb": 2000, "GPU-ccc": 3000, "GPU-ddd": 4000},
		{"GPU-aaa": 1400, "GPU-bbb": 2250, "GPU-ccc": 3150, "GPU-ddd": 4600},
		{"GPU-aaa": 1500, "GPU-bbb": 2250, "GPU-ccc": 3250, "GPU-ddd": 4650},
	}}

	k8s := k8sfake.NewSimpleClientset(
		runningGPUPod("pod-a", "vllm-model-a", portA),
		runningGPUPod("pod-b", "vllm-model-b", portB),
	)

	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	loop, err := NewMultiLoop(MultiConfig{
		Node:           "test-node",
		AggregatorAddr: addr,
		WindowDuration: time.Hour, // ticks are driven manually
		Energy:         &fakeEnergy{},
		PerDevice:      perDevice,
		K8s:            k8s,
		CheckpointPath: cpPath,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewMultiLoop: %v", err)
	}
	defer loop.Close() //nolint:errcheck

	ctx := context.Background()

	// Tick 1: baselines only — no reports.
	loop.tick(ctx)
	if n := len(svc.all()); n != 0 {
		t.Fatalf("reports after baseline tick = %d, want 0", n)
	}

	// Tick 2: first window. Energy deltas: pod-a 400+250=650, pod-b 150,
	// residual (GPU-ddd) 600. Token reads are baselines → zero deltas.
	loop.tick(ctx)
	first := svc.all()
	if len(first) != 3 {
		t.Fatalf("reports after first window = %d, want 3 (2 models + residual)", len(first))
	}
	byModel := map[string]*measurementv1.WindowReport{}
	for _, r := range first {
		byModel[r.ModelName] = r
	}
	assertReport := func(name string, wantJoules float64, wantTokens uint64) {
		t.Helper()
		r := byModel[name]
		if r == nil {
			t.Fatalf("no report for %q; got models %v", name, byModel)
		}
		if r.EnergyJoules != wantJoules {
			t.Errorf("%s joules = %v, want %v", name, r.EnergyJoules, wantJoules)
		}
		if r.OutputTokens != wantTokens {
			t.Errorf("%s tokens = %d, want %d", name, r.OutputTokens, wantTokens)
		}
		if r.Node != "test-node" {
			t.Errorf("%s node = %q", name, r.Node)
		}
	}
	assertReport("model-a", 650, 0) // TP=2: both its GPUs, nothing else
	assertReport("model-b", 150, 0)
	assertReport(model.ResidualModelName, 600, 0)

	// Tick 3: tokens advance; energy deltas: pod-a 100, pod-b 100, residual 50.
	tokensA.Store(1100) // +100
	tokensB.Store(5200) // +200
	loop.tick(ctx)
	all := svc.all()
	if len(all) != 6 {
		t.Fatalf("reports after second window = %d, want 6", len(all))
	}
	byModel = map[string]*measurementv1.WindowReport{}
	for _, r := range all[3:] {
		byModel[r.ModelName] = r
	}
	assertReport("model-a", 100, 100)
	assertReport("model-b", 100, 200)
	assertReport(model.ResidualModelName, 50, 0)
}
