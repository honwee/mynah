// Probe for the avatar-pool capacity view: reads real GPU state and prints the
// same budget breakdown the console shows, without needing an admin login.
//
// Run it on the host (or inside a container with a CDI GPU device, which is
// where nvidia-smi comes from).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"mynah/internal/avatarengine"
	"mynah/internal/gpu"
	"mynah/internal/supervisor"
)

func main() {
	socket := flag.String("socket", supervisor.DefaultSocket, "docker socket")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snap := gpu.Read(ctx)
	if !snap.Available {
		fmt.Printf("GPU unavailable: %s\n", snap.Reason)
		os.Exit(1)
	}

	// Attribute VRAM: pool workers' usage is reclaimable and must not count as
	// occupied, or the planner concludes the pool can never grow.
	poolPIDs := map[int]bool{}
	sup := supervisor.NewDocker(*socket)
	if list, err := sup.List(ctx); err == nil {
		for _, svc := range list {
			if strings.HasPrefix(svc.Name, "musetalk") && svc.HostPID > 0 {
				poolPIDs[svc.HostPID] = true
			}
		}
	}

	var gpus []avatarengine.GPU
	fmt.Printf("%-6s %-9s %-10s %-10s %-9s %s\n",
		"GPU", "TOTAL", "POOL", "OTHERS", "RESERVE", "USABLE")
	for _, d := range snap.Devices {
		byPool := snap.UsedByMB(d.Index, poolPIDs)
		others := d.UsedMB - byPool
		if others < 0 {
			others = 0
		}
		reserve := avatarengine.ReserveMB(d.TotalMB)
		usable := d.TotalMB - others - reserve
		if usable < 0 {
			usable = 0
		}
		gpus = append(gpus, avatarengine.GPU{
			Index: d.Index, TotalMB: d.TotalMB, UsedByOthersMB: others,
		})
		fmt.Printf("%-6d %-9d %-10d %-10d %-9d %d\n",
			d.Index, d.TotalMB, byPool, others, reserve, usable)
	}

	fmt.Println()
	for _, e := range avatarengine.All() {
		sel, why := e.Selectable()
		if !sel {
			fmt.Printf("%-22s UNAVAILABLE — %s\n", e.Label, why)
			continue
		}
		plan, err := avatarengine.Plan(e, gpus)
		if err != nil {
			fmt.Printf("%-22s plan error: %v\n", e.Label, err)
			continue
		}
		fmt.Printf("%-22s %dMB/worker -> max %d workers %v\n",
			e.Label, e.VRAMSteadyMB, plan.Total, plan.PerGPU)
		fmt.Printf("  %s\n", plan.Explain)
	}
}
