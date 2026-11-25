// Command fakesub2api runs the Sub2API fixture on a loopback address.
package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	browserSmoke := flags.Bool("browser-smoke", false, "seed browser smoke test data and accept events on stdin")
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
	var browserEvent func(fakesub2api.BrowserEvent) (time.Time, error)
	if *browserSmoke {
		handler, browserEvent = fakesub2api.NewBrowserSmoke(time.Now())
	} else {
		handler = fakesub2api.New(fakesub2api.Options{})
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if _, err := fmt.Fprintln(out, "http://"+listener.Addr().String()); err != nil {
		return err
	}
	if browserEvent != nil {
		go func() {
			scanner := bufio.NewScanner(in)
			eventID := 0
			for scanner.Scan() {
				if len(scanner.Bytes()) == 0 {
					at, _ := browserEvent(fakesub2api.BrowserEvent{Kind: "usage"})
					_, _ = fmt.Fprintln(out, "usage", at.Format(time.RFC3339Nano))
					continue
				}
				eventID++
				var event fakesub2api.BrowserEvent
				err := json.Unmarshal(scanner.Bytes(), &event)
				if err == nil {
					_, err = browserEvent(event)
				}
				if err != nil {
					_, _ = fmt.Fprintln(out, "event", eventID, err)
				} else {
					_, _ = fmt.Fprintln(out, "event", eventID)
				}
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
