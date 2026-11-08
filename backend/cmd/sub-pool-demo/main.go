// sub-pool-demo never loads production configuration, authentication or database packages.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sub-pool/backend/internal/demo"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := demo.Serve(ctx, os.Getenv("SUB_POOL_DEMO_LISTEN")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
