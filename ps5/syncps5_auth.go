// Copyright (C) 2026 syncps5 contributors. GPLv3 (see LICENSE).
//
// Syncthing on the PS5 never runs with an unauthenticated GUI.
//
//   - At start, if config.xml has no GUI user and password, Syncthing does not
//     start. Port 8384 serves a setup page instead. Setting a password there
//     requires a one-time code shown as a notification on the console's
//     screen, so only someone at the console can claim it.
//   - If the password is removed later, the GUI and REST API refuse every
//     request and Syncthing restarts into setup mode.

//go:build ps5

package main

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"html/template"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/syncthing/syncthing/cmd/syncthing/generate"
)

const (
	ps5ConfDir      = ps5Dir + "/home"
	ps5ConfigFile   = ps5ConfDir + "/config.xml"
	ps5SetupCode    = ps5Dir + "/setup-code.txt"
	ps5MinPassword  = 8
	ps5MaxAttempts  = 5
	ps5NotifyPeriod = 15 * time.Second
)


// ps5AuthConfigured reports whether config.xml enables GUI authentication,
// with the same rule as GUIConfiguration.IsAuthEnabled.
func ps5AuthConfigured() bool {
	data, err := os.ReadFile(ps5ConfigFile)
	if err != nil {
		return false
	}
	var cfg struct {
		GUI struct {
			User     string `xml:"user"`
			Password string `xml:"password"`
			AuthMode string `xml:"authMode"`
		} `xml:"gui"`
	}
	if err := xml.Unmarshal(data, &cfg); err != nil {
		// A config Syncthing cannot read either; let Syncthing report it.
		return true
	}
	g := cfg.GUI
	return strings.EqualFold(strings.TrimSpace(g.AuthMode), "ldap") ||
		(strings.TrimSpace(g.User) != "" && strings.TrimSpace(g.Password) != "")
}

// ps5RequirePassword blocks until GUI authentication is configured.
func ps5RequirePassword() {
	if ps5AuthConfigured() {
		_ = os.Remove(ps5SetupCode)
		return
	}
	fmt.Fprintf(os.Stderr, "[syncps5] no GUI password is set; Syncthing waits for first-run setup at %s\n", ps5GUIAddr())
	s := &ps5Setup{done: make(chan struct{})}
	s.newCode()
	s.serve()
	_ = os.Remove(ps5SetupCode)
	fmt.Fprintf(os.Stderr, "[syncps5] GUI password set; starting Syncthing\n")
}

type ps5Setup struct {
	mu         sync.Mutex
	code       string
	attempts   int
	lastNotify time.Time
	done       chan struct{}
	finished   bool
}

func (s *ps5Setup) newCode() {
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		panic(err)
	}
	s.code = fmt.Sprintf("%08d", n.Int64())
	s.attempts = 0
	// For a console without a screen at hand: readable only with file access
	// to the console. Never logged.
	_ = os.WriteFile(ps5SetupCode, []byte(s.code+"\n"), 0o600)
	s.lastNotify = time.Time{}
	s.notifyLocked()
}

// notifyLocked shows the code on the console, at most every ps5NotifyPeriod.
func (s *ps5Setup) notifyLocked() {
	if time.Since(s.lastNotify) < ps5NotifyPeriod {
		return
	}
	s.lastNotify = time.Now()
	ps5Notify("Syncthing setup code: %s\nEnter it at http://%s:%s to set the GUI password.", s.code, ps5LocalIP(), ps5GUIPort())
}

// ps5GUIAddr is where the GUI will listen (STGUIADDRESS, as set by the
// launcher); the setup page uses the same address.
func ps5GUIAddr() string {
	addr := os.Getenv("STGUIADDRESS")
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	if addr == "" {
		addr = "0.0.0.0:8384"
	}
	return addr
}

func ps5GUIPort() string {
	_, port, err := net.SplitHostPort(ps5GUIAddr())
	if err != nil {
		return "8384"
	}
	return port
}

func (s *ps5Setup) serve() {
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	var lastErr time.Time
	for {
		ln, err := net.Listen("tcp", ps5GUIAddr())
		if err != nil {
			if time.Since(lastErr) > time.Minute {
				fmt.Fprintf(os.Stderr, "[syncps5] setup: %v (retrying)\n", err)
				lastErr = time.Now()
			}
			time.Sleep(5 * time.Second)
			continue
		}
		fmt.Fprintf(os.Stderr, "[syncps5] setup page listening on %s\n", ps5GUIAddr())
		go func() { _ = srv.Serve(ln) }()
		select {
		case <-s.done:
			// Let the final response reach the browser, then free the port
			// for Syncthing's GUI.
			time.Sleep(time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
			return
		}
	}
}

type ps5SetupPage struct {
	Error    string
	User     string
	Done     bool
	MinLen   int
	Attempts int
}

func (s *ps5Setup) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	page := ps5SetupPage{MinLen: ps5MinPassword}

	if r.Method != http.MethodPost {
		if r.URL.Path != "/" {
			// Syncthing GUI assets and REST calls from an open tab.
			http.Error(w, "Syncthing is waiting for its GUI password to be set; open / to set it.", http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		s.notifyLocked()
		s.mu.Unlock()
		ps5SetupTmpl.Execute(w, page)
		return
	}

	// Slow down guessing.
	time.Sleep(time.Second)
	_ = r.ParseForm()
	code := strings.TrimSpace(r.PostFormValue("code"))
	user := strings.TrimSpace(r.PostFormValue("user"))
	pass := r.PostFormValue("password")
	pass2 := r.PostFormValue("password2")
	page.User = user

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.finished:
		page.Done = true
	case code != s.code:
		s.attempts++
		page.Error = "Wrong setup code. It is shown in a notification on the PS5 screen."
		fmt.Fprintf(os.Stderr, "[syncps5] setup: wrong code from %s (%d/%d)\n", r.RemoteAddr, s.attempts, ps5MaxAttempts)
		if s.attempts >= ps5MaxAttempts {
			s.newCode()
			page.Error = "Too many wrong codes. A new code is now shown on the PS5 screen."
		}
	case user == "":
		page.Error = "Enter a user name."
	case len(pass) < ps5MinPassword:
		page.Error = fmt.Sprintf("The password must be at least %d characters long.", ps5MinPassword)
	case pass != pass2:
		page.Error = "The passwords do not match."
	default:
		if err := generate.Generate(ps5ConfDir, user, pass, false); err != nil {
			fmt.Fprintf(os.Stderr, "[syncps5] setup: saving the password failed: %v\n", err)
			page.Error = "Saving the password failed: " + err.Error()
			break
		}
		if !ps5AuthConfigured() {
			page.Error = "The password was not saved; see /data/syncps5/syncthing.log."
			break
		}
		fmt.Fprintf(os.Stderr, "[syncps5] setup: GUI user %q set from %s\n", user, r.RemoteAddr)
		s.finished = true
		page.Done = true
		close(s.done)
	}
	ps5SetupTmpl.Execute(w, page)
}

// ps5RefuseWithoutAuth wraps Syncthing's GUI handler when the running
// configuration has no GUI authentication (the password was removed). Every
// request is refused, and Syncthing restarts into setup mode.
var ps5RestartOnce sync.Once

func ps5RefuseWithoutAuth(http.Handler) http.Handler {
	ps5RestartOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "[syncps5] GUI authentication was removed; restarting into setup mode\n")
		go func() {
			time.Sleep(2 * time.Second)
			// SIGHUP makes Syncthing exit with the restart code; the exit
			// hook relaunches the payload, which then asks for a password.
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		}()
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "No GUI password is set. Syncthing is restarting; reload in a moment to set one.", http.StatusServiceUnavailable)
	})
}

// ps5Notify shows a pop-up on the PS5 screen, as libkernel's
// sceKernelSendNotificationRequest does: a 0xC30 byte request with the
// message at 0x2D, written to /dev/notification0 (from ps5-tailscale).
func ps5Notify(format string, args ...any) {
	const size, offset = 0xC30, 0x2D
	var req [size]byte
	msg := fmt.Sprintf(format, args...)
	if len(msg) > size-offset-1 {
		msg = msg[:size-offset-1]
	}
	copy(req[offset:], msg)
	fd, err := syscall.Open("/dev/notification0", syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	_, _ = syscall.Write(fd, req[:])
}

func ps5LocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return "<ps5-ip>"
}

var ps5SetupTmpl = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Done}}<meta http-equiv="refresh" content="8;url=/">{{end}}
<title>Syncthing on PS5 – setup</title>
<style>
:root{color-scheme:light dark;--bg:#f4f5f7;--card:#fff;--fg:#1d2026;--muted:#5d6470;--accent:#0891d1;--err:#c0392b;--border:#d6d9de}
@media (prefers-color-scheme:dark){:root{--bg:#15171b;--card:#1f2227;--fg:#e8eaed;--muted:#a0a6b0;--border:#353a42;--err:#ff7a6b}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif;display:flex;justify-content:center;padding:32px 16px}
main{background:var(--card);border:1px solid var(--border);border-radius:12px;padding:28px;max-width:420px;width:100%}
h1{font-size:1.3rem;margin:0 0 6px}p{color:var(--muted);margin:0 0 18px}
label{display:block;font-weight:600;margin:14px 0 4px}input{width:100%;padding:10px;border:1px solid var(--border);border-radius:8px;background:transparent;color:inherit;font-size:1rem}
button{margin-top:22px;width:100%;padding:11px;border:0;border-radius:8px;background:var(--accent);color:#fff;font-size:1rem;font-weight:600;cursor:pointer}
.err{color:var(--err);font-weight:600;margin:12px 0 0}
</style></head><body><main>
{{if .Done}}
<h1>Password set</h1>
<p>Syncthing is starting. This page opens the Syncthing GUI in a few seconds; sign in with the user name and password you chose.</p>
{{else}}
<h1>Set up Syncthing on your PS5</h1>
<p>Syncthing does not start until its web GUI is protected by a password. Enter the setup code shown in a notification on the PS5 screen, then choose a user name and password.</p>
<form method="post" autocomplete="off">
<label for="code">Setup code</label><input id="code" name="code" inputmode="numeric" autocomplete="one-time-code" required>
<label for="user">User name</label><input id="user" name="user" value="{{.User}}" autocomplete="username" required>
<label for="password">Password</label><input id="password" name="password" type="password" minlength="{{.MinLen}}" autocomplete="new-password" required>
<label for="password2">Repeat password</label><input id="password2" name="password2" type="password" minlength="{{.MinLen}}" autocomplete="new-password" required>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<button type="submit">Set password and start Syncthing</button>
</form>
{{end}}
</main></body></html>
`))
