package demo

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

func Serve(ctx context.Context, address string) error {
	if address == "" {
		address = "127.0.0.1:8082"
	}
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	number, parseErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || parseErr != nil || number < 1 || number > 65535 {
		return errors.New("demo requires an explicit loopback IP and TCP port")
	}
	server := &http.Server{Addr: address, Handler: Handler{}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(deadline); err != nil {
			_ = server.Close()
			return errors.New("demo shutdown timed out")
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
