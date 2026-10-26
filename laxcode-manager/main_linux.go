package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "/etc/laxcode-manager/config.json", "manager configuration")
	flag.Parse()
	c, err := readConfig(*path)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("manager must run as root")
	}
	if err = os.MkdirAll(c.StateDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(c.StateDir, "manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another manager owns the ledger")
	}
	l, err := openLedger(filepath.Join(c.StateDir, "quota.db"), c.DailyLimit, c.GrantChunk)
	if err != nil {
		return err
	}
	defer l.db.Close()
	k, err := openKernel(c, l.id)
	if err != nil {
		return err
	}
	defer k.close()
	m := &manager{cfg: c, k: k, l: l, d: newDocker(c.DockerSocket), bindings: map[string]binding{}, wake: make(chan struct{}, 1)}
	defer m.cleanupWG.Wait()
	// Recover each user through the same path used by requests and refill events.
	if err = m.refresh(context.Background()); err != nil {
		k.disable()
		return err
	}
	if err = k.enable(c.LowWatermark); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(c.SocketPath), 0700); err != nil {
		return err
	}
	if info, e := os.Lstat(c.SocketPath); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("socket path exists and is not a socket")
		}
		if err = os.Remove(c.SocketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, err := net.Listen("unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(c.SocketPath, 0600); err != nil {
		return err
	}
	// systemd Type=notify means parent guard installed, not Docker discovery done.
	// Docker is ordered after this point to avoid a dependency cycle.
	if address := os.Getenv("NOTIFY_SOCKET"); address != "" {
		if strings.HasPrefix(address, "@") {
			address = "\x00" + address[1:]
		}
		conn, e := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
		if e != nil {
			return e
		}
		_, e = conn.Write([]byte("READY=1"))
		conn.Close()
		if e != nil {
			return e
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: m.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	select {
	case m.wake <- struct{}{}:
	default:
	}
	log.Printf("guard installed; control socket %s", c.SocketPath)
	select {
	case err = <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			<-done
			return fmt.Errorf("control API: %w", err)
		}
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	<-done
	// Shutdown never removes pins or detaches guards.
	if m.failure() == nil {
		return m.refresh(shutdown)
	}
	return nil
}
