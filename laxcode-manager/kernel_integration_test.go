//go:build linux && integration

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/google/uuid"
	"net/netip"
)

func TestNetworkHelper(t *testing.T) {
	if os.Getenv("QUOTA_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("QUOTA_CGROUP")+"/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(3)
	}
	endpoint := os.Getenv("QUOTA_ENDPOINT")
	if address := os.Getenv("QUOTA_SELF_LOOPBACK"); address != "" {
		if err := exec.Command("ip", "link", "set", "lo", "up").Run(); err != nil {
			os.Exit(4)
		}
		if address == "192.0.2.1" {
			if err := exec.Command("ip", "addr", "add", address+"/32", "dev", "lo").Run(); err != nil {
				os.Exit(5)
			}
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
		if err != nil {
			os.Exit(6)
		}
		endpoint = listener.Addr().String()
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				conn.Write(bytes.Repeat([]byte("x"), 128))
				conn.Close()
			}
		}()
	}
	conn, err := net.DialTimeout("tcp", endpoint, 300*time.Millisecond)
	if err != nil {
		fmt.Print("0")
		os.Exit(0)
	}
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	n, _ := io.Copy(io.Discard, conn)
	conn.Close()
	fmt.Print(n)
	os.Exit(0)
}

func TestKernelWorkloadLoopback(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root required")
	}
	c := defaultConfig()
	c.CgroupParent = fmt.Sprintf("/sys/fs/cgroup/laxcode-quota-loop-%d.slice", os.Getpid())
	c.PinPath = fmt.Sprintf("/sys/fs/bpf/laxcode-quota-loop-%d", os.Getpid())
	c.ControlPeers = []controlPeer{{"127.0.0.1", 9, "tcp"}}
	if err := os.Mkdir(c.CgroupParent, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(c.CgroupParent)
	k, err := openKernel(c, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { k.close(); os.RemoveAll(c.PinPath) }()
	if err := k.enable(c.LowWatermark); err != nil {
		t.Fatal(err)
	}
	group := filepath.Join(c.CgroupParent, "workload")
	if err := os.Mkdir(group, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(group)
	probe := func(address string) int {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNetworkHelper$")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
		cmd.Env = append(os.Environ(), "QUOTA_HELPER=1", "QUOTA_CGROUP="+group, "QUOTA_SELF_LOOPBACK="+address)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated loopback probe: %s %v", out, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if probe("127.0.0.1") != 0 {
		t.Fatal("unknown workload bypassed guard")
	}
	id, err := cgroupID(group)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.objects.Maps["cgroup_owner"].Update(id, uint64(0), ebpf.UpdateAny); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1", "::1"} {
		if n := probe(address); n != 128 {
			t.Fatalf("verified workload loopback %s blocked: %d", address, n)
		}
	}
	if probe("192.0.2.1") != 0 {
		t.Fatal("workload marker allowed non-loopback traffic")
	}
	k.disable()
	if probe("127.0.0.1") != 0 {
		t.Fatal("disabled guard allowed workload loopback")
	}
}
func TestKernelGuard(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root required")
	}
	parent := "/sys/fs/cgroup/laxcode-quota-test-" + strconv.Itoa(os.Getpid()) + ".slice"
	pin := "/sys/fs/bpf/laxcode-quota-test-" + strconv.Itoa(os.Getpid())
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(parent)
	c := defaultConfig()
	c.CgroupParent = parent
	c.PinPath = pin
	c.DailyLimit = 1 << 20
	c.GrantChunk = 128 << 10
	c.LowWatermark = 32 << 10
	server := func() net.Listener {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					conn.SetWriteDeadline(time.Now().Add(time.Second))
					conn.Write(bytes.Repeat([]byte("x"), 256<<10))
				}()
			}
		}()
		t.Cleanup(func() { ln.Close() })
		return ln
	}
	control, public := server(), server()
	c.ControlPeers = []controlPeer{{"127.0.0.1", uint16(control.Addr().(*net.TCPAddr).Port), "tcp"}}
	db := uuid.New()
	k, err := openKernel(c, db)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer func() { k.close(); os.RemoveAll(pin) }()
	if err = k.enable(c.LowWatermark); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]uint32{
		"8.8.8.8": 1, "1.1.1.1": 1, "192.0.0.9": 1, "192.0.0.11": 0,
		"127.0.0.1": 0, "169.254.169.254": 0, "100.96.0.96": 0, "100.100.100.200": 0,
		"192.168.1.1": 0, "10.1.2.3": 0, "198.18.0.1": 0, "240.0.0.1": 0,
		"::ffff:127.0.0.1": 0, "::ffff:8.8.8.8": 1, "::1": 0, "fd00::1": 0,
		"fe80::1": 0, "64:ff9b::808:808": 0, "2001:db8::1": 0, "2002:0808:0808::1": 0,
		"2001:4860:4860::8888": 1, "2606:4700:4700::1111": 1,
	} {
		var got uint32
		err = k.objects.Maps["address_class"].Lookup(lpmKey{128, addressBytes(netip.MustParseAddr(raw))}, &got)
		if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("classification %s=%d want %d", raw, got, want)
		}
	}
	var groups []string
	for i := 0; i < 2; i++ {
		p := filepath.Join(parent, fmt.Sprint("client", i))
		if err = os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		groups = append(groups, p)
		defer os.Remove(p)
	}
	probe := func(group string, ln net.Listener) int {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNetworkHelper$")
		cmd.Env = append(os.Environ(), "QUOTA_HELPER=1", "QUOTA_CGROUP="+group, "QUOTA_ENDPOINT="+ln.Addr().String())
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %s %v", raw, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(string(raw), err)
		}
		return n
	}
	if n := probe(groups[0], control); n != 256<<10 {
		t.Fatalf("control denied: %d", n)
	}
	if n := probe(groups[0], public); n != 0 {
		t.Fatalf("private allowed: %d", n)
	}
	// Local server stands in for a public peer only inside this isolated test map.
	if err = k.objects.Maps["address_class"].Update(prefixKey("127.0.0.1/32"), uint32(1), ebpf.UpdateAny); err != nil {
		t.Fatal(err)
	}
	if n := probe(groups[0], public); n != 0 {
		t.Fatalf("unknown cgroup allowed: %d", n)
	}
	now, _ := bootNS()
	e := epoch{ID: 1, User: 1, Deadline: now + uint64(30*time.Second)}
	if err = k.initialize(e); err != nil {
		t.Fatal(err)
	}
	if err = k.objects.Maps["grants"].Update(e.ID, grant{128 << 10, 1}, ebpf.UpdateExist); err != nil {
		t.Fatal(err)
	}
	if err = k.objects.Maps["user_bucket"].Update(e.User, e.ID, ebpf.UpdateAny); err != nil {
		t.Fatal(err)
	}
	for _, p := range groups {
		cg, err := cgroupID(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = k.objects.Maps["cgroup_owner"].Update(cg, e.User, ebpf.UpdateAny); err != nil {
			t.Fatal(err)
		}
	}
	first := probe(groups[0], public)
	second := probe(groups[1], public)
	u, _, err := k.snapshot(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first <= 0 || first+second > 128<<10 || u.RX == 0 || u.TX == 0 || u.RX+u.TX > 128<<10 || u.DropPackets == 0 {
		t.Fatalf("quota first=%d second=%d usage=%+v", first, second, u)
	}
	k.close()
	k, err = openKernel(c, db)
	if err != nil {
		t.Fatalf("reopen: %+v", err)
	}
	after, _, err := k.snapshot(e.ID)
	if err != nil || after.RX < u.RX || after.TX < u.TX {
		t.Fatalf("counters lost: %+v %v", after, err)
	}
	if n := probe(groups[1], control); n != 256<<10 {
		t.Fatalf("control counted: %d", n)
	}
	before, _, _ := k.snapshot(e.ID)
	if err = k.objects.Maps["bucket_meta"].Update(e.ID, bucketMeta{e.User, 1}, ebpf.UpdateExist); err != nil {
		t.Fatal(err)
	}
	if n := probe(groups[0], public); n != 0 {
		t.Fatal("expired day allowed")
	}
	after, _, _ = k.snapshot(e.ID)
	if after.RX != before.RX || after.TX != before.TX {
		t.Fatal("expired day charged")
	}
	t.Logf("shared quota payload=%d RX=%d TX=%d drops=%d; controls and pinned restart passed", first+second, u.RX, u.TX, u.DropPackets)
}
