package backhaul

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/utils"
)

// TestOpenTUN exercises the whole ioctl chain: create the interface, assign an
// address, set the peer and MTU, and bring it up. This is the part with no pure
// unit test, so running it for real is the only way to know the ifreq layouts
// are right.
func TestOpenTUN(t *testing.T) {
	requireRoot(t)
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("/dev/net/tun not available")
	}

	tun, err := openTUN("", "10.201.0.1", "10.201.0.2", 1300)
	if err != nil {
		t.Fatalf("openTUN: %v", err)
	}
	defer tun.Close()

	if tun.Name() == "" {
		t.Error("interface has no name")
	}
	if !strings.HasPrefix(tun.Name(), "tun") {
		t.Logf("interface named %q (kernel default)", tun.Name())
	}

	// The interface should now carry the address we assigned.
	iface, err := net.InterfaceByName(tun.Name())
	if err != nil {
		t.Fatalf("look up %s: %v", tun.Name(), err)
	}
	if iface.Flags&net.FlagUp == 0 {
		t.Error("interface is not up")
	}
	if iface.MTU != 1300 {
		t.Errorf("MTU = %d, want 1300", iface.MTU)
	}
	addrs, _ := iface.Addrs()
	found := false
	for _, a := range addrs {
		if strings.Contains(a.String(), "10.201.0.1") {
			found = true
		}
	}
	if !found {
		t.Errorf("assigned address not found on interface; addrs = %v", addrs)
	}
}

func TestRandomIPv4IsPlausible(t *testing.T) {
	for i := 0; i < 200; i++ {
		ip := randomIPv4().To4()
		if ip == nil {
			t.Fatal("randomIPv4 did not return an IPv4 address")
		}
		switch {
		case ip[0] == 0, ip[0] == 10, ip[0] == 127, ip[0] >= 224:
			t.Errorf("randomIPv4 produced a reserved-range address: %v", ip)
		}
	}
}

// TestEngineStartsAndStops brings up a real backhaul engine (TUN + UDP carrier)
// and confirms it starts, reports linked, and shuts down cleanly on context
// cancel.
func TestEngineStartsAndStops(t *testing.T) {
	requireRoot(t)

	cfg := config.BackhaulConfig{
		Carrier:  config.CarrierUDP,
		Port:     freeUDPPort(t),
		Token:    "shared",
		LocalIP:  "10.202.0.1",
		RemoteIP: "10.202.0.2",
		MTU:      1400,
	}
	eng, err := New(cfg, config.RoleServer, utils.NewLogger("error"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	// Give it a moment to bring up the interface and bind the carrier.
	time.Sleep(300 * time.Millisecond)
	if _, err := net.InterfaceByName("tun0"); err != nil {
		// The name may differ; just check some tun interface exists.
		ifaces, _ := net.Interfaces()
		any := false
		for _, i := range ifaces {
			if strings.HasPrefix(i.Name, "tun") {
				any = true
			}
		}
		if !any {
			t.Error("no tun interface came up while the engine was running")
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("engine returned an error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not stop within 3s of cancel")
	}
}
