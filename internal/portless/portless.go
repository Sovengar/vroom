// Package portless is the only place allowed to talk to the portless binary; vroom only registers routes, never serves them (see docs/adr/adr-0013-vroom-registers-portless-routes.md).
package portless

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds every binary call and probe because degradation must never be worse than not having the feature.
const DefaultTimeout = 5 * time.Second

// DefaultVerifyWait bounds how long Apply waits for a freshly written route to become served. portless exposes a new
// route to its proxy either through an fs.watch debounce (100ms) or, when the watcher is unavailable, through a 3s
// polling fallback; under CI's runner the watcher is unavailable, so a single immediate probe would always read the
// stale cache and degrade a route the proxy is about to serve. Waiting past the polling interval turns that env fact
// into a served url, and a proxy that already serves the route is never delayed (the first probe returns).
const DefaultVerifyWait = 3500 * time.Millisecond

// verifyPollInterval spaces the retries inside DefaultVerifyWait: small enough to catch the 100ms debounce quickly,
// large enough not to spin the proxy while the polling fallback counts down its 3s.
const verifyPollInterval = 250 * time.Millisecond

// Route status as published in JSON: registered means the URL was actually seen working, nothing less.
const (
	StatusRegistered = "registered"
	StatusDegraded   = "degraded"
)

// Degradation reasons are machine-readable because agents branch on them, so they stay stable and terse.
const (
	ReasonPortlessMissing  = "portless_not_found"
	ReasonPortlessFailed   = "portless_failed"
	ReasonPortlessTimeout  = "portless_timeout"
	ReasonProxyNotRunning  = "proxy_not_running"
	ReasonProxyUnreachable = "proxy_unreachable"
	ReasonRouteConflict    = "route_conflict"
	ReasonPortUnresolved   = "port_unresolved"
	ReasonInvalidName      = "route_invalid_name"
	ReasonRouteNotServed   = "route_not_served"
)

// ErrProxyNotRunning: the proxy was there and stopped, a fact distinct from a declared port that refuses connections; both degrade without a url.
var ErrProxyNotRunning = errors.New("portless: no proxy running (proxy.port absent)")

// ProbeOnce is the seam the tests drive against a real listener: a closed port returns an error, never a status.
func ProbeOnce(scheme, hostname string, proxyPort int, path string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return httpProbe(ctx, scheme, hostname, proxyPort, path)
}

// ExecFunc is the hermeticity seam for tests; the exit code travels apart from the error because portless exits 1 benignly on alias --remove of a missing name (M10).
type ExecFunc func(ctx context.Context, bin string, args ...string) (stdout string, exitCode int, err error)

// ProbeFunc dials loopback directly because .localhost name resolution is not guaranteed under a service manager.
type ProbeFunc func(ctx context.Context, scheme, hostname string, proxyPort int, path string) (status int, err error)

// Client keeps every binary, path and probe injectable because CI has no portless, no Node 24 and no proxy.
type Client struct {
	bin      string
	stateDir string
	exec     ExecFunc
	probe    ProbeFunc
	timeout  time.Duration
	// verifyWait is how long Apply keeps probing a route the proxy answered 404 for: 0 keeps the old single-probe behaviour and lets the deterministic fakes assert the degradation without a delay.
	verifyWait time.Duration
}

type ClientOption func(*Client)

func WithBinary(bin string) ClientOption { return func(c *Client) { c.bin = bin } }

func WithStateDir(dir string) ClientOption { return func(c *Client) { c.stateDir = dir } }

func WithExec(fn ExecFunc) ClientOption { return func(c *Client) { c.exec = fn } }

func WithProbe(fn ProbeFunc) ClientOption { return func(c *Client) { c.probe = fn } }

func WithTimeout(d time.Duration) ClientOption { return func(c *Client) { c.timeout = d } }

// WithVerifyWait overrides the propagation window Apply tolerates; 0 restores the single immediate probe the deterministic fakes rely on.
func WithVerifyWait(d time.Duration) ClientOption { return func(c *Client) { c.verifyWait = d } }

// New takes the binary and state dir already resolved by the caller and only defaults the seams, so path resolution stays in one place.
func New(opts ...ClientOption) *Client {
	c := &Client{timeout: DefaultTimeout, verifyWait: DefaultVerifyWait}
	for _, o := range opts {
		o(c)
	}
	if c.exec == nil {
		c.exec = execCommand
	}
	if c.probe == nil {
		c.probe = httpProbe
	}
	return c
}

// Default returns a binary-less client instead of an error, because a missing portless is a degradation that Apply turns into a warning.
func Default(opts ...ClientOption) *Client {
	opts = append([]ClientOption{
		WithBinary(ResolveBinary()),
		WithStateDir(ResolveStateDir()),
	}, opts...)
	return New(opts...)
}

func (c *Client) HasBinary() bool { return c.bin != "" }

func (c *Client) Binary() string { return c.bin }

func (c *Client) StateDir() string { return c.stateDir }

// ResolveStateDir matches what the CLI writes: $PORTLESS_STATE_DIR or $HOME/.portless. XDG_STATE_HOME is ignored by
// portless (measured 2026-10-07 on 0.15.6), so honouring it read proxy.port from a directory that never exists.
func ResolveStateDir() string {
	if v := os.Getenv("PORTLESS_STATE_DIR"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".portless")
	}
	return ""
}

const proxyPortFile = "proxy.port"

// ResolveBinary: a bare "portless" cannot be assumed, because a minimal PATH does not resolve it - it lives behind the mise shims (M14).
func ResolveBinary() string {
	if v := os.Getenv("PORTLESS_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("portless"); err == nil {
		return p
	}
	for _, dir := range miseShimDirs() {
		p := filepath.Join(dir, "portless")
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// miseShimDirs is derived from the running process HOME, never from a literal.
func miseShimDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "share", "mise", "shims"),
		filepath.Join(home, ".local", "bin"),
	}
}

// execCommand: WaitDelay is what makes the timeout bound wall-clock time, since a live descendant holding the pipes can hang Wait past the deadline.
func execCommand(ctx context.Context, bin string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	// The subcommand names the error and comes from args[0]; with no args the binary name stands in, because an index-out-of-range here would break startup.
	sub := filepath.Base(bin)
	if len(args) > 0 {
		sub = args[0]
	}
	if err != nil && ctx.Err() != nil {
		// A ctx error means the deadline killed the process, which is a different published reason than the binary failing on its own.
		return stdout.String(), code, fmt.Errorf("portless %s: %w", sub, ctx.Err())
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), code, fmt.Errorf("portless %s: %w: %s", sub, err, msg)
		}
		return stdout.String(), code, fmt.Errorf("portless %s: %w", sub, err)
	}
	return stdout.String(), code, nil
}

func (c *Client) run(args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.exec(ctx, c.bin, args...)
}

// Register is an unconditional upsert: the same name with another port overwrites silently and still exits 0 (M8), so its exit code proves nothing.
func (c *Client) Register(name string, port int) error {
	if c.bin == "" {
		return errors.New(ReasonPortlessMissing)
	}
	_, code, err := c.run("alias", name, strconv.Itoa(port))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s: portless alias exited %d", ReasonPortlessFailed, code)
	}
	return nil
}

// ErrRouteAbsent is portless's benign exit 1 "No alias found" (M10): the route is gone, which is success for Remove but must stay distinct so only the successful case revokes ownership.
var ErrRouteAbsent = errors.New("portless: no route with that name")

// Remove treats the benign missing-name exit 1 as nil because a repeated stop must not fail; RemoveAbsent is where that distinction is needed.
func (c *Client) Remove(name string) error {
	if name == "" {
		return nil
	}
	if c.bin == "" {
		return nil
	}
	_, code, err := c.run("alias", "--remove", name)
	if code == 1 && isRouteAbsent(err) {
		return nil
	}
	if err != nil && code == 0 {
		return err
	}
	return nil
}

// RemoveAbsent propagates every other exit 1 (Node too old, EACCES, corrupt JSON) because those may leave the route behind, and losing the handle would orphan it.
func (c *Client) RemoveAbsent(name string) error {
	if name == "" {
		return nil
	}
	if c.bin == "" {
		return nil
	}
	_, code, err := c.run("alias", "--remove", name)
	switch {
	case err == nil && code == 0:
		return nil
	case code == 1 && isRouteAbsent(err):
		return ErrRouteAbsent
	default:
		if err == nil {
			err = fmt.Errorf("portless alias --remove %s: exited %d", name, code)
		}
		return err
	}
}

// isRouteAbsent tells portless's benign missing-name message apart from a real failure, which would leave the route in place.
func isRouteAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no alias found")
}

// routeLineRE parses the measured shape of "portless list": the left port belongs to the PROXY, the right one is where the route actually points.
var routeLineRE = regexp.MustCompile(`http://([^\s:]+):\d+\s+->\s+localhost:(\d+)`)

// Lookup parses "list" and never routes.json, because the state file is internal and unversioned while the CLI is the contract; the returned port is what Apply compares to detect conflicts (M8).
func (c *Client) Lookup(name string) (port int, found bool, err error) {
	if c.bin == "" {
		return 0, false, errors.New(ReasonPortlessMissing)
	}
	out, code, err := c.run("list")
	if err != nil {
		return 0, false, err
	}
	if code != 0 {
		return 0, false, fmt.Errorf("%s: portless list exited %d", ReasonPortlessFailed, code)
	}
	host := Hostname(name)
	for _, m := range routeLineRE.FindAllStringSubmatch(out, -1) {
		if m[1] == host {
			p, convErr := strconv.Atoi(m[2])
			if convErr != nil {
				return 0, false, fmt.Errorf("puerto ilegible %q en portless list: %w", m[2], convErr)
			}
			return p, true, nil
		}
	}
	return 0, false, nil
}

// ProxyPort: proxy.port exists only while the proxy runs (M6), so its absence IS the no-proxy signal and no port is ever assumed.
func (c *Client) ProxyPort() (int, error) {
	if c.stateDir == "" {
		return 0, ErrProxyNotRunning
	}
	raw, err := os.ReadFile(filepath.Join(c.stateDir, proxyPortFile))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrProxyNotRunning
		}
		return 0, fmt.Errorf("could not read portless %s: %w", proxyPortFile, err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || port <= 0 || port > 65535 {
		// A corrupt proxy.port is indistinguishable from a stopped proxy here, so degrade.
		return 0, ErrProxyNotRunning
	}
	return port, nil
}

// httpProbe returns whatever status came back, because 502 proves the proxy routes this host while 404 proves it does not; only an error means "no answer at all".
func httpProbe(ctx context.Context, scheme, hostname string, proxyPort int, path string) (int, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{
			// InsecureSkipVerify does not cancel ServerName: the SNI still goes out, which is how the proxy picks the route; only the certificate chain is forgone, irrelevant on loopback.
			InsecureSkipVerify: true,
			ServerName:         hostname,
		},
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+hostname+path, nil)
	if err != nil {
		return 0, err
	}
	req.Host = hostname
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
