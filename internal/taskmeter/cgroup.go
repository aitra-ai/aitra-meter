package taskmeter

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrCgroupNotFound indicates the cgroup path does not exist or has no recognizable CPU accounting files.
	ErrCgroupNotFound = errors.New("cgroup CPU accounting file not found")

	// ErrCgroupMalformed indicates the cgroup accounting file exists but cannot be parsed as valid numeric metrics.
	ErrCgroupMalformed = errors.New("cgroup CPU accounting file is malformed")
)

// CgroupMetrics holds parsed cgroup CPU usage metrics in microseconds.
type CgroupMetrics struct {
	UsageUsec  uint64 `json:"usage_usec"`
	UserUsec   uint64 `json:"user_usec,omitempty"`
	SystemUsec uint64 `json:"system_usec,omitempty"`
	Version    string `json:"version"` // "v2" or "v1"
}

// CgroupReader parses container CPU usage from Linux cgroup v1/v2 files.
type CgroupReader struct {
	cgroupRoot string
}

// NewCgroupReader creates a CgroupReader with the specified cgroup root (defaults to /sys/fs/cgroup).
func NewCgroupReader(cgroupRoot string) *CgroupReader {
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}
	return &CgroupReader{cgroupRoot: cgroupRoot}
}

// ReadUsageUsec resolves the specified cgroup path (or "self" / empty for auto-detect)
// and returns the accumulated CPU usage in microseconds.
func (r *CgroupReader) ReadUsageUsec(targetPath string) (uint64, error) {
	metrics, err := r.ReadMetrics(targetPath)
	if err != nil {
		return 0, err
	}
	return metrics.UsageUsec, nil
}

// ReadMetrics resolves the specified cgroup path and returns complete CgroupMetrics.
func (r *CgroupReader) ReadMetrics(targetPath string) (CgroupMetrics, error) {
	target := strings.TrimSpace(targetPath)

	// Resolve path
	resolvedPath, isV2, err := r.resolveCPUPath(target)
	if err != nil {
		return CgroupMetrics{}, err
	}

	if isV2 {
		return r.parseCgroupV2(resolvedPath)
	}
	return r.parseCgroupV1(resolvedPath)
}

// resolveCPUPath locates the actual file to read:
// Returns (filePath, isV2, error).
func (r *CgroupReader) resolveCPUPath(target string) (string, bool, error) {
	// If empty or "self", check container root or /sys/fs/cgroup/cpu.stat first
	if target == "" || target == "self" {
		target = r.cgroupRoot
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(r.cgroupRoot, target)
	}

	// 1. Check if target is already a direct file (e.g. .../cpu.stat or .../cpuacct.usage)
	info, err := os.Stat(target)
	if err != nil {
		// Heuristic: target might be a container ID or scope name under system.slice or docker
		base := filepath.Base(target)
		matches, _ := filepath.Glob(filepath.Join(r.cgroupRoot, "system.slice", "docker-"+base+"*.scope"))
		if len(matches) > 0 {
			target = matches[0]
			info, err = os.Stat(target)
		}
		if err != nil {
			matches, _ := filepath.Glob(filepath.Join(r.cgroupRoot, "docker", base+"*"))
			if len(matches) > 0 {
				target = matches[0]
				info, err = os.Stat(target)
			}
		}
		if err != nil {
			return "", false, fmt.Errorf("%w: %s (%v)", ErrCgroupNotFound, target, err)
		}
	}

	if !info.IsDir() {
		baseName := filepath.Base(target)
		if baseName == "cpu.stat" {
			return target, true, nil
		}
		if baseName == "cpuacct.usage" || strings.HasSuffix(baseName, ".usage") {
			return target, false, nil
		}
		// If custom file, try v2 first
		return target, true, nil
	}

	// 2. Target is a directory: check cgroup v2 cpu.stat first
	v2File := filepath.Join(target, "cpu.stat")
	if _, err := os.Stat(v2File); err == nil {
		return v2File, true, nil
	}

	// 3. Fallback to cgroup v1 cpuacct.usage
	v1File := filepath.Join(target, "cpuacct.usage")
	if _, err := os.Stat(v1File); err == nil {
		return v1File, false, nil
	}

	// Check cgroup v1 with cpuacct subdirectory if under root
	v1SubFile := filepath.Join(target, "cpuacct", "cpuacct.usage")
	if _, err := os.Stat(v1SubFile); err == nil {
		return v1SubFile, false, nil
	}

	v1CpuSubFile := filepath.Join(target, "cpu,cpuacct", "cpuacct.usage")
	if _, err := os.Stat(v1CpuSubFile); err == nil {
		return v1CpuSubFile, false, nil
	}

	return "", false, fmt.Errorf("%w: neither cpu.stat nor cpuacct.usage found in %s", ErrCgroupNotFound, target)
}

// parseCgroupV2 parses key-value pairs from cgroup v2 cpu.stat
func (r *CgroupReader) parseCgroupV2(path string) (CgroupMetrics, error) {
	f, err := os.Open(path)
	if err != nil {
		return CgroupMetrics{}, fmt.Errorf("%w: cannot open %s (%v)", ErrCgroupNotFound, path, err)
	}
	defer f.Close()

	var m CgroupMetrics
	m.Version = "v2"
	foundUsage := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}

		switch parts[0] {
		case "usage_usec":
			m.UsageUsec = val
			foundUsage = true
		case "user_usec":
			m.UserUsec = val
		case "system_usec":
			m.SystemUsec = val
		}
	}

	if err := scanner.Err(); err != nil {
		return CgroupMetrics{}, fmt.Errorf("%w: read %s (%v)", ErrCgroupMalformed, path, err)
	}
	if !foundUsage {
		return CgroupMetrics{}, fmt.Errorf("%w: 'usage_usec' key missing in %s", ErrCgroupMalformed, path)
	}

	return m, nil
}

// parseCgroupV1 parses a single integer (nanoseconds) from cgroup v1 cpuacct.usage
func (r *CgroupReader) parseCgroupV1(path string) (CgroupMetrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CgroupMetrics{}, fmt.Errorf("%w: cannot read %s (%v)", ErrCgroupNotFound, path, err)
	}

	strVal := strings.TrimSpace(string(data))
	nanos, err := strconv.ParseUint(strVal, 10, 64)
	if err != nil {
		return CgroupMetrics{}, fmt.Errorf("%w: %s content %q is not a valid uint64 (%v)", ErrCgroupMalformed, path, strVal, err)
	}

	// Convert nanoseconds to microseconds
	return CgroupMetrics{
		UsageUsec: nanos / 1000,
		Version:   "v1",
	}, nil
}
