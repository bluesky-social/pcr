package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

type oneConnListener struct {
	conn      net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	accepted  bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		conn := l.conn
		l.mu.Unlock()
		return conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestServeUntilShutdownWaitsForActiveRequest(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseRequest:
		default:
			close(releaseRequest)
		}
	})

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(requestStarted)
			<-releaseRequest
			_, _ = io.WriteString(w, "done")
		}),
	}
	serverConn, clientConn := net.Pipe()
	listener := &oneConnListener{conn: serverConn, closed: make(chan struct{})}
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = srv.Close() })

	shutdownCh := make(chan os.Signal, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveUntilShutdown(srv, listener, shutdownCh, 5*time.Second)
	}()

	requestDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://pipe/", nil)
		if err == nil {
			req.Close = true
			err = req.Write(clientConn)
		}
		var resp *http.Response
		if err == nil {
			resp, err = http.ReadResponse(bufio.NewReader(clientConn), req)
		}
		if err == nil {
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			err = errors.Join(readErr, closeErr)
		}
		requestDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach handler")
	}
	shutdownCh <- syscall.SIGTERM

	select {
	case err := <-serveDone:
		t.Fatalf("serveUntilShutdown() returned before handler completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRequest)
	if err := <-requestDone; err != nil {
		t.Fatalf("request error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("serveUntilShutdown() error = %v", err)
	}
}
