// Copyright (C) 2026 syncps5 contributors. GPLv3 (see LICENSE).
//
// PS5 integration for Syncthing. The build copies this file into
// cmd/syncthing of the Syncthing tree, so it is part of package main and
// runs before Syncthing's own main().

//go:build ps5

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	// The PS5 has no CA bundle; without this, HTTPS (relay pool, global
	// discovery over HTTPS, usage reporting) fails to verify certificates.
	_ "golang.org/x/crypto/x509roots/fallback"
	"golang.org/x/net/route"
)

const (
	ps5Dir        = "/data/syncps5"
	ps5Payload    = ps5Dir + "/syncps5.elf"
	ps5Log        = ps5Dir + "/syncthing.log"
	ps5LogAddr    = ":8385"
	ps5ElfLoader  = "127.0.0.1:9021"
)

func init() {
	ps5SetupStdio()
	ps5SetupResolver()
	go ps5ServeLogs()

	// Syncthing runs as `serve` by default, but be explicit.
	if len(os.Args) <= 1 {
		os.Args = append(os.Args, "serve")
	}

	// Without a monitor process, a restart requested from the GUI (or by a
	// config change) ends the process. Relaunch through the ELF loader.
	ps5ExitHook = func(code int) {
		const exitRestart = 3
		if code != exitRestart {
			return
		}
		if _, err := os.Stat(ps5Payload); err != nil {
			fmt.Fprintf(os.Stderr, "[syncps5] restart requested but %s is missing; not relaunching\n", ps5Payload)
			return
		}
		conn, err := net.DialTimeout("tcp", ps5ElfLoader, 3*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[syncps5] relaunch: %v\n", err)
			return
		}
		fmt.Fprintf(conn, "file:%s\n", ps5Payload)
		fmt.Fprintf(os.Stderr, "[syncps5] relaunching via ELF loader\n")
		// The new instance's launcher stops this process if it is still
		// around; give the loader a moment to read the request.
		time.Sleep(500 * time.Millisecond)
		conn.Close()
	}
}

// The payload's stdin, stdout and stderr are the ELF loader's TCP
// connection. Go kills a process whose write to fd 1 or 2 fails with EPIPE,
// so after a short hello the connection is dropped and 1 and 2 point at the
// log file instead. Syncthing runs without its monitor process (the PS5
// cannot fork/exec), so it logs to stdout and rotation is done here.
func ps5SetupStdio() {
	_ = os.MkdirAll(ps5Dir, 0o755)
	hello := fmt.Sprintf("[syncps5] Syncthing is starting; log: %s (tcp port 8385)\n", ps5Log)
	_, _ = syscall.Write(1, []byte(hello)) // raw write: no EPIPE handling

	ps5RotateLog(4 << 20)
	fd, err := syscall.Open(ps5Log, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_APPEND, 0o644)
	if err != nil {
		return
	}
	null, err := syscall.Open("/dev/null", syscall.O_RDONLY, 0)
	if err == nil {
		_ = syscall.Dup2(null, 0)
		_ = syscall.Close(null)
	}
	if st, err := os.Stat(ps5Log); err == nil {
		ps5LogStart = st.Size()
	}
	_ = syscall.Dup2(fd, 1)
	_ = syscall.Dup2(fd, 2)
	_ = syscall.Close(fd)
	fmt.Fprintf(os.Stderr, "\n[syncps5] ==== start %s pid %d, %s ====\n", time.Now().UTC().Format(time.RFC3339), os.Getpid(), os.Getenv("SYNCPS5"))

	go func() {
		for {
			time.Sleep(time.Minute)
			ps5RotateLog(16 << 20)
		}
	}()
}

// ps5LogStart is where this instance's output begins in the log; a log
// client gets everything from there on.
var ps5LogStart int64

// ps5RotateLog keeps one old copy of the log once it grows past max bytes.
// The writers hold the file open with O_APPEND, so it is copied and
// truncated in place rather than renamed.
func ps5RotateLog(max int64) {
	st, err := os.Stat(ps5Log)
	if err != nil || st.Size() <= max {
		return
	}
	src, err := os.Open(ps5Log)
	if err != nil {
		return
	}
	defer src.Close()
	dst, err := os.Create(ps5Log + ".old")
	if err != nil {
		return
	}
	_, _ = io.Copy(dst, src)
	dst.Close()
	_ = os.Truncate(ps5Log, 0)
	ps5LogStart = 0
}

// ---------------------------------------------------------------------------
// DNS

// The PS5 has no /etc/resolv.conf, so Go's resolver would only ask
// 127.0.0.1:53. Probe a few servers (the default gateway first) and use the
// first one that answers; re-probe periodically.

var (
	ps5DNSMu     sync.Mutex
	ps5DNSServer = "1.1.1.1:53"
)

func ps5SetupResolver() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			ps5DNSMu.Lock()
			server := ps5DNSServer
			ps5DNSMu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
	go func() {
		for {
			ps5ProbeDNS()
			time.Sleep(5 * time.Minute)
		}
	}()
}

func ps5ProbeDNS() {
	candidates := []string{"127.0.0.1:53"}
	if gw := ps5DefaultGateway(); gw != "" {
		candidates = append(candidates, net.JoinHostPort(gw, "53"))
	}
	candidates = append(candidates, "1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53")
	for _, c := range candidates {
		if ps5DNSAnswers(c) {
			ps5DNSMu.Lock()
			changed := ps5DNSServer != c
			ps5DNSServer = c
			ps5DNSMu.Unlock()
			if changed {
				fmt.Fprintf(os.Stderr, "[syncps5] using DNS server %s\n", c)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "[syncps5] no DNS server answered; tried %v\n", candidates)
}

func ps5DNSAnswers(server string) bool {
	conn, err := net.DialTimeout("udp", server, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	// Query: A record for discovery.syncthing.net
	q := []byte{0x51, 0x7a, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range []string{"discovery", "syncthing", "net"} {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1)
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := conn.Write(q); err != nil {
		return false
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	return err == nil && n >= 12 && buf[0] == 0x51 && buf[1] == 0x7a
}

func ps5DefaultGateway() string {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return ""
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || len(rm.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		dst, ok1 := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		gw, ok2 := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
		if ok1 && ok2 && dst.IP == [4]byte{} && gw.IP != [4]byte{} {
			return net.IP(gw.IP[:]).String()
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Log server

// Anyone connecting to port 8385 gets this instance's log so far, then
// everything written to it as it happens.
func ps5ServeLogs() {
	for {
		ln, err := net.Listen("tcp", ps5LogAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[syncps5] log server: %v\n", err)
			time.Sleep(10 * time.Second)
			continue
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				// Sony returns errno 163 when the network is reconfigured;
				// the listener has to be reopened.
				fmt.Fprintf(os.Stderr, "[syncps5] log server accept: %v\n", err)
				break
			}
			go ps5StreamLogs(c)
		}
		ln.Close()
		time.Sleep(time.Second)
	}
}

func ps5StreamLogs(c net.Conn) {
	defer c.Close()
	var mu sync.Mutex
	out := func(p []byte) error {
		mu.Lock()
		defer mu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := c.Write(p)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Detect the client going away.
		_, _ = io.Copy(io.Discard, c)
		cancel()
	}()
	ps5Follow(ctx, ps5Log, "", out)
}

// ps5Follow sends the lines of path from where this instance started (at
// most the last 64 KiB of them), then the lines appended to it, and follows
// rotation.
func ps5Follow(ctx context.Context, path, prefix string, out func([]byte) error) {
	var (
		f       *os.File
		ino     uint64
		off     int64
		pending []byte
		first   = true
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	buf := make([]byte, 32<<10)
	for ctx.Err() == nil {
		st, err := os.Stat(path)
		if err == nil {
			sys, _ := st.Sys().(*syscall.Stat_t)
			var curIno uint64
			if sys != nil {
				curIno = uint64(sys.Ino)
			}
			if f == nil || curIno != ino || st.Size() < off {
				if f != nil {
					f.Close()
				}
				f, err = os.Open(path)
				if err != nil {
					f = nil
				} else {
					ino, off = curIno, 0
					if first {
						off = max(ps5LogStart, st.Size()-64<<10, 0)
						if off > st.Size() {
							off = 0
						}
					}
					first = false
				}
			}
		}
		if f != nil {
			for {
				n, err := f.ReadAt(buf, off)
				if n > 0 {
					off += int64(n)
					pending = append(pending, buf[:n]...)
				}
				if err != nil || n == 0 {
					break
				}
			}
			for {
				i := indexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := append([]byte(prefix), pending[:i+1]...)
				pending = pending[i+1:]
				if out(line) != nil {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

