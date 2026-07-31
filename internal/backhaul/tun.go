package backhaul

import (
	"crypto/rand"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// tunDevice is an open TUN interface whose reads and writes are raw IP packets.
// Creating and addressing it needs CAP_NET_ADMIN, so like the raw carrier it
// runs as root.
//
// The interface is created and brought up here directly through ioctls rather
// than by shelling out to `ip`, so backfire has no dependency on iproute2 being
// installed — which it often is not on a minimal VPS image.
//
// The fd is used with blocking unix.Read/unix.Write rather than being wrapped in
// an *os.File. An *os.File registers the descriptor with Go's runtime network
// poller, and TUN character devices reject that — the first Read fails with
// "not pollable". Talking to the raw fd sidesteps the poller entirely.
type tunDevice struct {
	fd   int
	name string
}

// openTUN creates a TUN interface named name (or an auto-assigned name when
// blank), assigns localIP, sets the peer and MTU and brings it up.
func openTUN(name, localIP, remoteIP string, mtu int) (*tunDevice, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}

	// TUNSETIFF: IFF_TUN (layer 3) | IFF_NO_PI (no packet-info prefix).
	var ifr [unix.IFNAMSIZ + 64]byte
	copy(ifr[:unix.IFNAMSIZ], name)
	const iffTun, iffNoPI = 0x0001, 0x1000
	flags := uint16(iffTun | iffNoPI)
	ifr[unix.IFNAMSIZ] = byte(flags)
	ifr[unix.IFNAMSIZ+1] = byte(flags >> 8)

	if err := ioctl(uintptr(fd), unix.TUNSETIFF, unsafe.Pointer(&ifr[0])); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF: %w", err)
	}
	// Read back the name the kernel actually assigned.
	actual := string(trimZero(ifr[:unix.IFNAMSIZ]))

	dev := &tunDevice{fd: fd, name: actual}
	if err := dev.configure(localIP, remoteIP, mtu); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return dev, nil
}

// configure assigns the address, sets the MTU and flips the interface up, all
// through a throwaway control socket.
func (d *tunDevice) configure(localIP, remoteIP string, mtu int) error {
	ctrl, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer unix.Close(ctrl)

	if err := d.setAddr(ctrl, unix.SIOCSIFADDR, localIP); err != nil {
		return fmt.Errorf("set address: %w", err)
	}
	// Point-to-point peer address, so the kernel routes remoteIP over this link.
	if remoteIP != "" {
		if err := d.setAddr(ctrl, unix.SIOCSIFDSTADDR, remoteIP); err != nil {
			return fmt.Errorf("set peer address: %w", err)
		}
	}
	if err := d.setMTU(ctrl, mtu); err != nil {
		return fmt.Errorf("set mtu: %w", err)
	}
	if err := d.setUp(ctrl); err != nil {
		return fmt.Errorf("bring up: %w", err)
	}
	return nil
}

// ifreqAddr is the ifreq layout used for the address-setting ioctls.
type ifreqAddr struct {
	name [unix.IFNAMSIZ]byte
	fam  uint16
	port uint16
	addr [4]byte
	_    [8]byte
}

func (d *tunDevice) setAddr(ctrl int, request uint, ip string) error {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return fmt.Errorf("%q is not an IPv4 address", ip)
	}
	var req ifreqAddr
	copy(req.name[:], d.name)
	req.fam = unix.AF_INET
	copy(req.addr[:], parsed)
	return ioctl(uintptr(ctrl), request, unsafe.Pointer(&req))
}

func (d *tunDevice) setMTU(ctrl, mtu int) error {
	var req struct {
		name [unix.IFNAMSIZ]byte
		mtu  int32
		_    [20]byte
	}
	copy(req.name[:], d.name)
	req.mtu = int32(mtu)
	return ioctl(uintptr(ctrl), unix.SIOCSIFMTU, unsafe.Pointer(&req))
}

func (d *tunDevice) setUp(ctrl int) error {
	var req struct {
		name  [unix.IFNAMSIZ]byte
		flags uint16
		_     [22]byte
	}
	copy(req.name[:], d.name)
	req.flags = unix.IFF_UP | unix.IFF_RUNNING | unix.IFF_POINTOPOINT | unix.IFF_NOARP
	return ioctl(uintptr(ctrl), unix.SIOCSIFFLAGS, unsafe.Pointer(&req))
}

// Read returns the next IP packet from the interface. It blocks in the read
// syscall directly; Close unblocks it by closing the descriptor.
func (d *tunDevice) Read(p []byte) (int, error) { return unix.Read(d.fd, p) }

// Write injects an IP packet into the interface.
func (d *tunDevice) Write(p []byte) (int, error) { return unix.Write(d.fd, p) }

// Name is the interface name the kernel assigned.
func (d *tunDevice) Name() string { return d.name }

// Close removes the interface (it is deleted when its last fd closes).
func (d *tunDevice) Close() error { return unix.Close(d.fd) }

func ioctl(fd uintptr, request uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(request), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func trimZero(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// randomIPv4 returns a random routable-looking IPv4 address, used as the forged
// source when spoofing is on but no explicit source was configured.
func randomIPv4() net.IP {
	var b [4]byte
	_, _ = rand.Read(b[:])
	// Reject the reserved first octets rather than remapping, since a remap can
	// itself land back on a reserved value.
	for reservedFirstOctet(b[0]) {
		var one [1]byte
		_, _ = rand.Read(one[:])
		b[0] = one[0]
	}
	return net.IPv4(b[0], b[1], b[2], b[3])
}

// reservedFirstOctet reports whether an octet begins a range we should not use
// for a plausible-looking public source: this-network, private 10/8, loopback,
// and the multicast/reserved space at 224+.
func reservedFirstOctet(o byte) bool {
	return o == 0 || o == 10 || o == 127 || o >= 224
}
