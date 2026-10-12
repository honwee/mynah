// Probe for the Docker supervisor: lists the managed services exactly as the
// console's /services endpoint would, without needing an admin login.
//
// Verifies the risky part — that the Engine API integration works and that the
// container-name allowlist maps onto what is actually running.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"mynah/internal/supervisor"
)

func main() {
	socket := flag.String("socket", supervisor.DefaultSocket, "docker socket")
	action := flag.String("action", "", "optional: start|stop|restart to exercise a mutation")
	name := flag.String("name", "", "service name for -action")
	flag.Parse()

	sup := supervisor.NewDocker(*socket)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if *action != "" {
		if *name == "" {
			fmt.Println("-action requires -name")
			os.Exit(2)
		}
		var err error
		switch *action {
		case "start":
			err = sup.Start(ctx, *name)
		case "stop":
			err = sup.Stop(ctx, *name)
		case "restart":
			err = sup.Restart(ctx, *name)
		default:
			fmt.Println("unknown action")
			os.Exit(2)
		}
		if err != nil {
			fmt.Printf("%s %s FAILED: %v\n", *action, *name, err)
			os.Exit(1)
		}
		fmt.Printf("%s %s OK\n", *action, *name)
		return
	}

	list, err := sup.List(ctx)
	if err != nil {
		fmt.Printf("list failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%-12s %-10s %-6s %-5s %-14s %s\n", "NAME", "STATE", "PORT", "GPU", "MODE", "DETAIL")
	for _, s := range list {
		fmt.Printf("%-12s %-10s %-6d %-5d %-14s %s\n", s.Name, s.State, s.Port, s.GPU, s.Mode, s.Detail)
	}

	// The allowlist must reject anything not in the registry — that is the
	// security boundary between an HTTP caller and the docker socket.
	for _, bad := range []string{"postgres", "../../etc", "mynah-cored", ""} {
		if err := sup.Start(ctx, bad); err == nil {
			fmt.Printf("SECURITY FAIL: allowlist accepted %q\n", bad)
			os.Exit(1)
		}
	}
	fmt.Println("allowlist rejects non-registry names: OK")
}
