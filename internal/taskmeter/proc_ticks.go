package taskmeter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcStat represents relevant fields parsed from /proc/<pid>/stat.
type ProcStat struct {
	PID    int
	PPID   int
	Utime  uint64
	Stime  uint64
	Cutime uint64
	Cstime uint64
}

// ProcReader parses Linux /proc virtual files for system and process statistics.
type ProcReader struct {
	root string
}

// NewProcReader creates a ProcReader rooted at root (defaults to "/proc").
func NewProcReader(root string) *ProcReader {
	if root == "" {
		root = "/proc"
	}
	return &ProcReader{root: root}
}

// SystemActiveTicks reads /proc/stat and returns (activeTicks, totalTicks, error).
// Active ticks sum user, nice, system, irq, softirq, and steal time across all cores.
func (r *ProcReader) SystemActiveTicks() (active uint64, total uint64, err error) {
	statPath := filepath.Join(r.root, "stat")
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", statPath, err)
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			// fields[0] is "cpu"
			// fields: [cpu, user, nice, system, idle, iowait, irq, softirq, steal, guest, guest_nice]
			if len(fields) < 5 {
				return 0, 0, fmt.Errorf("malformed /proc/stat cpu line: %q", line)
			}
			user, _ := strconv.ParseUint(fields[1], 10, 64)
			nice, _ := strconv.ParseUint(fields[2], 10, 64)
			system, _ := strconv.ParseUint(fields[3], 10, 64)
			idle, _ := strconv.ParseUint(fields[4], 10, 64)

			var iowait, irq, softirq, steal uint64
			if len(fields) > 5 {
				iowait, _ = strconv.ParseUint(fields[5], 10, 64)
			}
			if len(fields) > 6 {
				irq, _ = strconv.ParseUint(fields[6], 10, 64)
			}
			if len(fields) > 7 {
				softirq, _ = strconv.ParseUint(fields[7], 10, 64)
			}
			if len(fields) > 8 {
				steal, _ = strconv.ParseUint(fields[8], 10, 64)
			}

			active = user + nice + system + irq + softirq + steal
			total = active + idle + iowait
			return active, total, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, errors.New("aggregate cpu line not found in /proc/stat")
}

// ProcessStat reads and parses /proc/<pid>/stat safely.
func (r *ProcReader) ProcessStat(pid int) (*ProcStat, error) {
	path := filepath.Join(r.root, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return parsePidStat(data)
}

// parsePidStat parses raw bytes of /proc/<pid>/stat.
// Handles command names with spaces or parentheses by scanning for the last ')'.
func parsePidStat(data []byte) (*ProcStat, error) {
	s := string(data)
	lastParen := strings.LastIndexByte(s, ')')
	if lastParen == -1 || lastParen+2 > len(s) {
		return nil, fmt.Errorf("malformed pid stat (no closing paren): %q", s)
	}

	// Extract PID before '('
	firstParen := strings.IndexByte(s, '(')
	if firstParen == -1 {
		return nil, fmt.Errorf("malformed pid stat (no opening paren): %q", s)
	}
	pidStr := strings.TrimSpace(s[:firstParen])
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid pid %q: %w", pidStr, err)
	}

	// Remainder after ') '
	rest := strings.TrimSpace(s[lastParen+1:])
	fields := strings.Fields(rest)
	// fields indexes:
	// 0: state (field 3)
	// 1: ppid  (field 4)
	// ...
	// 11: utime  (field 14)
	// 12: stime  (field 15)
	// 13: cutime (field 16)
	// 14: cstime (field 17)
	if len(fields) < 15 {
		return nil, fmt.Errorf("insufficient fields in /proc/<pid>/stat (got %d, need >= 15)", len(fields))
	}

	ppid, _ := strconv.Atoi(fields[1])
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	cutime, _ := strconv.ParseUint(fields[13], 10, 64)
	cstime, _ := strconv.ParseUint(fields[14], 10, 64)

	return &ProcStat{
		PID:    pid,
		PPID:   ppid,
		Utime:  utime,
		Stime:  stime,
		Cutime: cutime,
		Cstime: cstime,
	}, nil
}

// ProcessTreeTicks recursively collects active ticks for rootPID and all its living descendants.
// Returns (totalTicks, found, error). If rootPID does not exist, returns (0, false, nil).
func (r *ProcReader) ProcessTreeTicks(rootPID int) (uint64, bool, error) {
	rootStat, err := r.ProcessStat(rootPID)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}

	// Scan /proc to discover child processes
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return 0, false, fmt.Errorf("readdir %s: %w", r.root, err)
	}

	parentToChildren := make(map[int][]int)
	cachedStats := make(map[int]*ProcStat)
	cachedStats[rootPID] = rootStat

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if pid == rootPID {
			continue
		}
		stat, err := r.ProcessStat(pid)
		if err != nil {
			// Process may have exited while scanning, skip
			continue
		}
		cachedStats[pid] = stat
		parentToChildren[stat.PPID] = append(parentToChildren[stat.PPID], pid)
	}

	// BFS traverse process tree starting from rootPID
	visited := make(map[int]bool)
	queue := []int{rootPID}
	visited[rootPID] = true

	var totalTicks uint64
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		stat := cachedStats[curr]
		if stat != nil {
			totalTicks += stat.Utime + stat.Stime + stat.Cutime + stat.Cstime
		}

		for _, child := range parentToChildren[curr] {
			if !visited[child] {
				visited[child] = true
				queue = append(queue, child)
			}
		}
	}

	return totalTicks, true, nil
}
