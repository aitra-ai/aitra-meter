package taskmeter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupReader_ParseV2(t *testing.T) {
	tmpDir := t.TempDir()
	v2Content := `usage_usec 54321000
user_usec 40000000
system_usec 14321000
nr_periods 12
nr_throttled 0
throttled_usec 0
`
	statFile := filepath.Join(tmpDir, "cpu.stat")
	if err := os.WriteFile(statFile, []byte(v2Content), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewCgroupReader(tmpDir)

	// 1. Read via directory
	metrics, err := reader.ReadMetrics(tmpDir)
	if err != nil {
		t.Fatalf("ReadMetrics(dir) failed: %v", err)
	}
	if metrics.Version != "v2" {
		t.Errorf("expected version v2, got %s", metrics.Version)
	}
	if metrics.UsageUsec != 54321000 {
		t.Errorf("expected UsageUsec 54321000, got %d", metrics.UsageUsec)
	}
	if metrics.UserUsec != 40000000 {
		t.Errorf("expected UserUsec 40000000, got %d", metrics.UserUsec)
	}
	if metrics.SystemUsec != 14321000 {
		t.Errorf("expected SystemUsec 14321000, got %d", metrics.SystemUsec)
	}

	// 2. Read via direct file path
	usec, err := reader.ReadUsageUsec(statFile)
	if err != nil {
		t.Fatalf("ReadUsageUsec(file) failed: %v", err)
	}
	if usec != 54321000 {
		t.Errorf("expected usec 54321000, got %d", usec)
	}

	// 3. Read via "self" (using tmpDir as cgroupRoot)
	selfUsec, err := reader.ReadUsageUsec("self")
	if err != nil {
		t.Fatalf("ReadUsageUsec(self) failed: %v", err)
	}
	if selfUsec != 54321000 {
		t.Errorf("expected self usec 54321000, got %d", selfUsec)
	}
}

func TestCgroupReader_ParseV1(t *testing.T) {
	tmpDir := t.TempDir()
	v1Content := "12500000000\n" // 12.5 seconds = 12,500,000 usec
	usageFile := filepath.Join(tmpDir, "cpuacct.usage")
	if err := os.WriteFile(usageFile, []byte(v1Content), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewCgroupReader(tmpDir)

	metrics, err := reader.ReadMetrics(tmpDir)
	if err != nil {
		t.Fatalf("ReadMetrics(dir v1) failed: %v", err)
	}
	if metrics.Version != "v1" {
		t.Errorf("expected version v1, got %s", metrics.Version)
	}
	if metrics.UsageUsec != 12500000 {
		t.Errorf("expected UsageUsec 12500000, got %d", metrics.UsageUsec)
	}
}

func TestCgroupReader_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	reader := NewCgroupReader(tmpDir)

	// Non-existent target
	_, err := reader.ReadMetrics(filepath.Join(tmpDir, "does-not-exist"))
	if !errors.Is(err, ErrCgroupNotFound) {
		t.Errorf("expected ErrCgroupNotFound, got %v", err)
	}

	// Missing usage key in v2
	badV2 := filepath.Join(tmpDir, "bad-v2")
	_ = os.MkdirAll(badV2, 0o755)
	_ = os.WriteFile(filepath.Join(badV2, "cpu.stat"), []byte("nr_periods 5\n"), 0o644)
	_, err = reader.ReadMetrics(badV2)
	if !errors.Is(err, ErrCgroupMalformed) {
		t.Errorf("expected ErrCgroupMalformed for missing key, got %v", err)
	}

	// Non-numeric v1
	badV1 := filepath.Join(tmpDir, "bad-v1")
	_ = os.MkdirAll(badV1, 0o755)
	_ = os.WriteFile(filepath.Join(badV1, "cpuacct.usage"), []byte("invalid_nanos\n"), 0o644)
	_, err = reader.ReadMetrics(badV1)
	if !errors.Is(err, ErrCgroupMalformed) {
		t.Errorf("expected ErrCgroupMalformed for non-numeric, got %v", err)
	}
}

func TestCgroupReader_ContainerIDMatching(t *testing.T) {
	cgroupRoot := t.TempDir()
	fullCID := "658a396b9a535f55a871b60e097bb40a3dbeed395a7c4ce2f3dcfde8308736ad"
	shortCID := "658a396b9a53"

	// Create system.slice/docker-<fullCID>.scope/cpu.stat
	scopeDir := filepath.Join(cgroupRoot, "system.slice", "docker-"+fullCID+".scope")
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statContent := "usage_usec 987654\n"
	if err := os.WriteFile(filepath.Join(scopeDir, "cpu.stat"), []byte(statContent), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewCgroupReader(cgroupRoot)

	// 1. Resolve using short CID
	mShort, err := reader.ReadMetrics(shortCID)
	if err != nil {
		t.Fatalf("failed to resolve short CID: %v", err)
	}
	if mShort.UsageUsec != 987654 {
		t.Errorf("expected 987654, got %d", mShort.UsageUsec)
	}

	// 2. Resolve using full CID
	mFull, err := reader.ReadMetrics(fullCID)
	if err != nil {
		t.Fatalf("failed to resolve full CID: %v", err)
	}
	if mFull.UsageUsec != 987654 {
		t.Errorf("expected 987654, got %d", mFull.UsageUsec)
	}
}
