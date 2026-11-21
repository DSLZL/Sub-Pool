// Command fakesub2api runs the Sub2API fixture on a loopback address.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sub-pool/backend/internal/fakesub2api"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("fakesub2api", flag.ContinueOnError)
	address := flags.String("listen", "127.0.0.1:0", "loopback IP and port")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	host, _, err := net.SplitHostPort(*address)
	ip, _ := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || flags.NArg() != 0 {
		return errors.New("listen requires a loopback IP and port")
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: fakesub2api.New(fakesub2api.Options{}), ReadHeaderTimeout: 5 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if _, err := fmt.Fprintln(out, "http://"+listener.Addr().String()); err != nil {
		return err
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
