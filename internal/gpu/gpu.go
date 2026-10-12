// Package gpu reads live NVIDIA GPU state so the control plane can size the
// avatar worker pool against real free memory instead of a guess.
//
// It shells out to nvidia-smi rather than binding NVML: cored gets the binary
// for free from the CDI device injection (/etc/cdi/nvidia.yaml mounts the host
// nvidia-smi), and this is a once-per-request read on a human timescale, so the
// cost of a subprocess is irrelevant next to a cgo dependency.
//
// cored does NOT initialize CUDA — it only needs visibility, so it never shows
// up as a compute process on the cards it is reporting.
package gpu

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Device is one GPU's memory picture, in MiB.
type Device struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	TotalMB int    `json:"total_mb"`
	UsedMB  int    `json:"used_mb"`
	FreeMB  int    `json:"free_mb"`
}

// Process is one compute process's VRAM claim. PID is the HOST pid, which is
// what lets callers attribute usage to a container (docker's .State.Pid is the
// same namespace).
type Process struct {
	PID    int `json:"pid"`
	UsedMB int `json:"used_mb"`
	Device int `json:"device"`
}

// Snapshot is one reading of every device plus the processes on them.
type Snapshot struct {
	Devices   []Device  `json:"devices"`
	Processes []Process `json:"processes"`
	Available bool      `json:"available"` // false = no nvidia-smi / no driver
	Reason    string    `json:"reason,omitempty"`
}

// Read queries nvidia-smi. A missing binary or driver is NOT an error — it
// yields Available=false with a reason, so a CPU-only host degrades to "cannot
// compute capacity" rather than breaking the console.
func Read(ctx context.Context) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	devs, err := readDevices(ctx)
	if err != nil {
		return Snapshot{Available: false, Reason: err.Error()}
	}
	procs, err := readProcesses(ctx, devs)
	if err != nil {
		// Devices read fine but per-process didn't: still useful (free memory
		// is known), just can't attribute usage.
		return Snapshot{Devices: devs, Available: true,
			Reason: "per-process usage unavailable: " + err.Error()}
	}
	return Snapshot{Devices: devs, Processes: procs, Available: true}
}

func readDevices(ctx context.Context) ([]Device, error) {
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,name,memory.total,memory.used,memory.free",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi unavailable: %w", err)
	}
	var devs []Device
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := splitCSV(sc.Text(), 5)
		if f == nil {
			continue
		}
		devs = append(devs, Device{
			Index:   atoi(f[0]),
			Name:    f[1],
			TotalMB: atoi(f[2]),
			UsedMB:  atoi(f[3]),
			FreeMB:  atoi(f[4]),
		})
	}
	if len(devs) == 0 {
		return nil, fmt.Errorf("nvidia-smi reported no devices")
	}
	return devs, nil
}

// readProcesses lists compute processes. nvidia-smi's compute-apps query does
// not include the device index in older drivers, so gpu_uuid is resolved back
// to an index via the device list.
func readProcesses(ctx context.Context, devs []Device) ([]Process, error) {
	uuidToIdx, err := deviceUUIDs(ctx)
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-compute-apps=pid,used_memory,gpu_uuid",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	var procs []Process
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := splitCSV(sc.Text(), 3)
		if f == nil {
			continue
		}
		idx, ok := uuidToIdx[f[2]]
		if !ok {
			idx = -1
		}
		procs = append(procs, Process{PID: atoi(f[0]), UsedMB: atoi(f[1]), Device: idx})
	}
	return procs, nil
}

func deviceUUIDs(ctx context.Context) (map[string]int, error) {
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,uuid", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	m := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := splitCSV(sc.Text(), 2)
		if f == nil {
			continue
		}
		m[f[1]] = atoi(f[0])
	}
	return m, nil
}

// UsedByMB sums the VRAM held by the given host PIDs on one device. Used to
// separate "the pool's own workers" from everything else, because the pool's
// current usage is reclaimable when resizing and must NOT count as occupied.
func (s Snapshot) UsedByMB(device int, pids map[int]bool) int {
	total := 0
	for _, p := range s.Processes {
		if p.Device == device && pids[p.PID] {
			total += p.UsedMB
		}
	}
	return total
}

// ForPIDs reports the VRAM held by a group of host pids (one container's
// processes) and the device they sit on. found is false when none of them holds
// a CUDA context — which is NOT the same as 0 MiB: a worker still loading its
// model has no reading at all, and showing it as "0MiB" would read as "this
// worker is free" when it may be about to take 7GB.
func (s Snapshot) ForPIDs(pids map[int]bool) (usedMB, device int, found bool) {
	device = -1
	for _, p := range s.Processes {
		if !pids[p.PID] {
			continue
		}
		usedMB += p.UsedMB
		if !found {
			device = p.Device
			found = true
		}
	}
	return usedMB, device, found
}

func splitCSV(line string, want int) []string {
	parts := strings.Split(line, ",")
	if len(parts) < want {
		return nil
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// atoi parses the leading integer, tolerating nvidia-smi's occasional unit
// suffixes and "[N/A]" values (which become 0 rather than panicking).
func atoi(s string) int {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(f[0])
	return n
}
