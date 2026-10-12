// Probe the avatar preload broadcast end to end: catalog -> resolve -> every
// worker in the pool.
//
// Also exercises the degradation path that matters in a rolling upgrade: a
// worker running an image without the Preload RPC must report a clear reason
// rather than looking like a network failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/avatarcatalog"
	"mynah/internal/avatarscan"
	"mynah/internal/engineclient"
)

func main() {
	bakes := flag.String("bakes", "", "avatar bakes dir")
	addrs := flag.String("workers", "127.0.0.1:9414,127.0.0.1:9417", "comma-separated worker addrs")
	avatar := flag.String("avatar", "", "avatar id to preload (default: first discovered)")
	flag.Parse()

	found := avatarscan.ScanBakes(*bakes)
	avatarcatalog.SetBaked(found)
	fmt.Printf("discovered %d baked avatars:\n", len(found))
	for _, b := range found {
		fmt.Printf("  %-22s engine=%-10s %s\n", b.ID, b.Engine, b.Path)
	}
	if len(found) == 0 {
		return
	}

	id := *avatar
	if id == "" {
		id = found[0].ID
	}
	path := avatarcatalog.ResolveAny(id, "")
	fmt.Printf("\nresolve %q -> %q\n", id, path)
	if path == "" {
		fmt.Println("FAIL: unresolved")
		return
	}
	fmt.Printf("engine required: %q\n\n", avatarcatalog.EngineFor(id))

	for _, addr := range splitCSV(*addrs) {
		cli, err := engineclient.Dial(addr)
		if err != nil {
			fmt.Printf("%-20s dial failed: %v\n", addr, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		t0 := time.Now()
		rep, err := cli.AE.Preload(ctx, &pb.PreloadRequest{Avatar: path})
		cancel()
		cli.Close()
		if err != nil {
			fmt.Printf("%-20s Preload error (%.1fs): %v\n", addr, time.Since(t0).Seconds(), err)
			continue
		}
		fmt.Printf("%-20s ready=%v already=%v load_ms=%d detail=%q\n",
			addr, rep.Ready, rep.Already, rep.LoadMs, rep.Detail)
	}
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r != ' ' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
