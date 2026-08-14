//go:build darwin

// The Darwin helper is intentionally a separately signed executable. It does
// not accept stdin, command-line paths, or unauthenticated requests; its native
// XPC listener is the only supported entry point.
package main

import (
	"fmt"
	"os"

	"github.com/goflasher/goflasher/internal/privilege"
)

func main() {
	fmt.Fprintf(os.Stderr, "GoFlasher privileged helper protocol %d requires its authenticated XPC service entry point\n", privilege.ProtocolVersion)
	os.Exit(78)
}
