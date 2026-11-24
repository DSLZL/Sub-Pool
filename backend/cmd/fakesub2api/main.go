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

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("fakesub2api", flag.ContinueOnError)
	address := flags.String("listen", "127.0.0.1:0", "loopback IP and port")
	browserSmoke := flags.Bool("browser-smoke", false, "seed browser smoke test data and accept one usage event on stdin")
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
	var handler http.Handler
	var recordUsage func() time.Time
	if *browserSmoke {
		handler, recordUsage = fakesub2api.NewBrowserSmoke(time.Now())
	} else {
		handler = fakesub2api.New(fakesub2api.Options{})
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if _, err := fmt.Fprintln(out, "http://"+listener.Addr().String()); err != nil {
		return err
	}
	if recordUsage != nil {
		go func() {
			var event [1]byte
			if _, err := io.ReadFull(in, event[:]); err == nil && event[0] == '\n' {
				_, _ = fmt.Fprintln(out, "usage", recordUsage().Format(time.RFC3339Nano))
			}
		}()
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
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
