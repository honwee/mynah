// admind: standalone control-plane binary — the open-core split seam.
//
// In the OSS single-binary deployment the control plane is embedded in cored
// (--db + --admin-listen). This cmd exists so the future EE module can ship
// its own assembly (multi-tenant auth, scheduling, training pipelines) by
// importing the app + core packages and registering richer
// implementations — without ever patching OSS sources.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, `admind: standalone control plane is not split out yet.
Use the embedded one: cored --db <dsn> --admin-listen 127.0.0.1:9080`)
	os.Exit(1)
}
