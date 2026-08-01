// Package e2e drives a full server+client tunnel in-process over the loopback
// interface and asserts that bytes sent to a published port arrive at the
// origin target and back. It runs the whole stack — transport, token handshake,
// mux and stream framing — for every transport the build supports.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/client"
	"github.com/thenawid/backfire/internal/server"
	"github.com/thenawid/backfire/internal/utils"
)

// freePort grabs an ephemeral loopback port and releases it, returning the
// number so a config can bind it a moment later.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startEcho serves an echo responder on a loopback port and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return l.Addr().String()
}

func runTunnel(t *testing.T, transport config.Transport) {
	t.Helper()
	log := utils.NewLogger("error")

	echoAddr := startEcho(t)
	tunnelPort := freePort(t)
	publicPort := freePort(t)
	token := utils.GenToken(16)

	srvCfg := config.ServerConfig{
		Bind:      fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Transport: transport,
		Token:     token,
		Forwards:  []string{fmt.Sprintf("127.0.0.1:%d=%s", publicPort, echoAddr)},
		KeepAlive: 10,
		Mux:       config.MuxConfig{},
	}
	// Fill mux/reconnect defaults the way Load would.
	full := &config.Config{Role: config.RoleServer, Server: srvCfg}
	if err := roundTripDefaults(full); err != nil {
		t.Fatalf("server config: %v", err)
	}
	srvCfg = full.Server

	cliCfg := config.ClientConfig{
		Server:    fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Transport: transport,
		Token:     token,
		KeepAlive: 10,
	}
	fullC := &config.Config{Role: config.RoleClient, Client: cliCfg}
	if err := roundTripDefaults(fullC); err != nil {
		t.Fatalf("client config: %v", err)
	}
	cliCfg = fullC.Client

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := server.New(srvCfg, "v0.0.0-test", log)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	go srv.Run(ctx)
	go client.New(cliCfg, "v0.0.0-test", log).Run(ctx)

	// Give the client a moment to dial in and link.
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", publicPort), 500*time.Millisecond)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		// A linked tunnel echoes; an unlinked one is dropped immediately.
		if echoes(conn) {
			conn.Close()
			return
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("tunnel never carried an echo for transport %s", transport)
}

// echoes writes a probe and reports whether it comes back intact.
func echoes(conn net.Conn) bool {
	msg := []byte("backfire-probe")
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		return false
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return false
	}
	return string(buf) == string(msg)
}

// roundTripDefaults runs a config through Save+Load so it picks up the same
// defaults and validation the real engine applies.
func roundTripDefaults(c *config.Config) error {
	dir, err := writeTemp(c)
	if err != nil {
		return err
	}
	loaded, err := config.Load(dir)
	if err != nil {
		return err
	}
	*c = *loaded
	return nil
}

// writeTemp saves a config to a temp file and returns its path.
func writeTemp(c *config.Config) (string, error) {
	dir, err := os.MkdirTemp("", "backfire-e2e")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "tunnel.toml")
	if err := config.Save(path, c); err != nil {
		return "", err
	}
	return path, nil
}

// TestTunnelAllTransports drives a real tunnel over every transport the build
// supports — both multiplexed and pooled, both TCP- and UDP-based — and asserts
// a byte round-trip through the whole stack each time.
func TestTunnelAllTransports(t *testing.T) {
	for _, tr := range config.KnownTransports {
		tr := tr
		t.Run(string(tr), func(t *testing.T) {
			t.Parallel()
			runTunnel(t, tr)
		})
	}
}
