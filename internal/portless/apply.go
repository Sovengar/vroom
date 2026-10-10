package portless

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"vroom/internal/manifest"
)

// Releaser exists because Release builds its own client, so without an injection point deleting its call sites left the suite green.
type Releaser interface {
	// RemoveAbsent, not Remove: Remove is benign by contract, and revoking ownership must tell "was not there" from "really failed".
	RemoveAbsent(name string) error
}

type ReleaserFunc func(name string) error

func (f ReleaserFunc) RemoveAbsent(name string) error { return f(name) }

// inertReleaser guards a test binary that never installed a seam, because a nil Releaser means the real client and would mutate the developer's own routes.json.
var inertReleaser = ReleaserFunc(func(string) error { return nil })

func InertReleaser() Releaser { return inertReleaser }

func IsTestBinary() bool { return strings.HasSuffix(os.Args[0], ".test") }

// ClientFor returns nil before resolving anything when the generation publishes no URL, because that early return is the backwards-compatibility gate and three copies of it would drift.
func ClientFor(gen string) *Client {
	if !manifest.PublishesURL(gen) {
		return nil
	}
	// A test binary must behave as if portless were not installed: this gateway is where every repo caller (TUI, CLI,
	// stacks engine) resolves the real binary, and without the gate a TUI test exec'd `portless alias` against the
	// developer's ~/.portless (measured 2026-10-07: the suite rewrote routes.json on every run). A bin-less client
	// still produces the documented missing-binary degradation, so tests keep asserting the real warning.
	if IsTestBinary() {
		return New()
	}
	return Default()
}

// Release's bool means ownership revoked, not success: a route that was already absent (M10) revokes too, and a real failure keeps the handle so the route stays reconcilable.
func Release(r Releaser, name string) bool {
	if name == "" {
		return true
	}
	if r == nil {
		r = Default()
	}
	err := r.RemoveAbsent(name)
	return err == nil || errors.Is(err, ErrRouteAbsent)
}

// Result is deliberately tri-state: Url is published only when it was seen working, because an agent reading an unverified url connects to something else.
type Result struct {
	Name   string
	Host   string
	Status string
	Url    string
	Reason string
	Port   int
	// Written is not the same as verified: an unverified route is still ours and must keep its handle.
	Registered bool
	// HeldPort is the port a conflicting route points at, so a fallback warning can say WHO holds the stable name; 0 when there was no conflict to read.
	HeldPort int
}

func (r Result) Succeeded() bool { return r.Status == StatusRegistered }

func Degraded(name, reason string) Result {
	return Result{Name: name, Host: Hostname(name), Status: StatusDegraded, Reason: reason}
}

// Ownership is a revocable lease, not a port: a persisted port never expires, so anyone reusing the name with it would inherit the right to be stomped.
type Ownership struct {
	Owned bool
	// Port is the port recorded at registration, never the live one: it is the proof the name is still ours.
	Port int
}

// Authorises exists as a method because a port match alone is the bug: ownership is what authorises, and only while it is unrevoked.
func (o Ownership) Authorises(existing int) bool {
	return o.Owned && o.Port > 0 && o.Port == existing
}

// Apply registers the route and reports what it could PROVE, never what was asked; no failure may reach startup, because service health never depends on its route.
func (c *Client) Apply(name string, port int, prev Ownership) Result {
	return c.ApplyContext(context.Background(), name, port, prev)
}

// ApplyContext is Apply with a caller-owned lifetime. Verification retries the probe for a bounded window (verifyWait), so an abandoned caller — a cancelled command, a TUI shutting down — must be able to stop at once instead of sleeping the rest of the window; without a context there is nothing to cut that wait. A nil context is treated as context.Background() rather than panicking inside context.WithTimeout.
func (c *Client) ApplyContext(ctx context.Context, name string, port int, prev Ownership) Result {
	if ctx == nil {
		// Exported seam: a nil context from a caller must degrade to "no cancellation", not crash the start.
		ctx = context.Background()
	}
	if !c.HasBinary() {
		return Degraded(name, ReasonPortlessMissing)
	}
	if port <= 0 {
		// Nothing honest to point at: registering an unresolved port only buys an error window (M9).
		return Degraded(name, ReasonPortUnresolved)
	}

	existing, found, err := c.Lookup(name)
	switch {
	case err != nil:
		return Degraded(name, classify(err))
	case found && existing != port && !prev.Authorises(existing):
		// Someone else's port with no proof it is still ours: it could be another app or another running vroom, so it stays untouched.
		return Result{Name: name, Host: Hostname(name), Status: StatusDegraded, Reason: ReasonRouteConflict, HeldPort: existing}
	}

	if err := c.Register(name, port); err != nil {
		return Degraded(name, classify(err))
	}

	// From here the write happened; later degradation must not undo it, so Registered is set here and not in the final return.
	registered := Result{Name: name, Host: Hostname(name), Port: port, Registered: true}

	// Read-back confirms the write and covers the window between the lookup and the write, when another actor can take the name (M8).
	published, found, err := c.Lookup(name)
	switch {
	case err != nil:
		// Cannot even read back: assert nothing, but the write happened, so ownership is kept.
		return registered.withReason(classify(err))
	case !found:
		return registered.withReason(ReasonRouteNotServed)
	case published != port:
		return registered.withReason(ReasonRouteConflict)
	}

	return c.verify(ctx, name, port)
}

// withReason degrades the state without undoing the fact: a route written while the proxy was down is still ours.
func (r Result) withReason(reason string) Result {
	r.Status = StatusDegraded
	r.Reason = reason
	r.Url = ""
	return r
}

// verify probes https then http instead of assuming a scheme, so the TLS/443 case needs no guess and the round cost is the two schemes. It retries for verifyWait because a freshly written route reaches the proxy's cache asynchronously: through the fs.watch debounce when the watcher works, or through the 3s polling fallback when it does not, and a single immediate probe would read the stale cache and degrade a route the proxy is about to serve (the exact CI failure: an inotify-starved runner falls back to polling). The wait is the caller's to cut: ctx cancellation stops the retries at once.
func (c *Client) verify(ctx context.Context, name string, port int) Result {
	host := Hostname(name)

	// M6: a missing proxy.port is the no-proxy signal, never a hardcoded 1355; this degrades after Register, so the route stays ours.
	proxyPort, err := c.ProxyPort()
	if err != nil {
		r := Degraded(name, ReasonProxyNotRunning)
		r.Registered = true
		return r
	}

	deadline := time.Now().Add(c.verifyWait)
	// answered records that the proxy itself replied (a 404), which is already proof it is reachable: only a request that answers nothing reaches acceptsConnections.
	answered := false
	for {
		replied := false
		for _, scheme := range []string{"https", "http"} {
			status, err := c.probeWithTimeout(ctx, scheme, host, proxyPort, probePath)
			if err != nil {
				continue
			}
			replied = true
			if status == 404 {
				// Measured: the proxy answers 404 when it does not know the host and 502 when it routes to a dead backend, so treating 404 as proof of routing would publish an address the proxy does not serve; it may still be a not-yet-served write, so keep probing until the window closes.
				answered = true
				continue
			}
			// Any other status, 502 included, proves the proxy routes this route; Registered lives in this literal so verify cannot forget it if it is ever called from elsewhere.
			return Result{
				Name:       name,
				Host:       host,
				Status:     StatusRegistered,
				Url:        scheme + "://" + host,
				Port:       port,
				Registered: true,
			}
		}
		// A proxy whose declared port refuses connections can never serve the route, so waiting the whole window is pointless; only a proxy that is up but slow to reload deserves it.
		if !replied && !c.acceptsConnections(proxyPort) {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		// An abandoned caller stops here instead of sleeping: select cuts the bout as soon as ctx is done.
		if !c.wait(ctx, verifyPollInterval) {
			break
		}
	}

	if answered {
		// The proxy answered for the host and still does not route it, so this is the not-served degradation, exactly as before the wait.
		return Degraded(name, ReasonRouteNotServed)
	}
	if !c.acceptsConnections(proxyPort) {
		r := Degraded(name, ReasonProxyUnreachable)
		r.Registered = true
		return r
	}
	r := Degraded(name, ReasonRouteNotServed)
	r.Registered = true
	return r
}

// probePath is "/" because even a 404 from the app behind proves routing, and health_path is a TUI contract a route may not have.
const probePath = "/"

func (c *Client) probeWithTimeout(ctx context.Context, scheme, host string, proxyPort int, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.probe(ctx, scheme, host, proxyPort, path)
}

// wait sleeps at most d and reports whether the full bout elapsed; a done context cuts it short, so an abandoned caller never pays the rest of the propagation window.
func (c *Client) wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// acceptsConnections separates "the declared proxy port answers nobody" from "the proxy answers but does not know the route", which degrade with different reasons.
func (c *Client) acceptsConnections(proxyPort int) bool {
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)), c.timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// classify: a timeout gets its own reason because the warning differs - portless did not answer is not portless failed.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonPortlessTimeout
	case errors.Is(err, ErrProxyNotRunning):
		return ReasonProxyNotRunning
	case isMissingBinary(err):
		return ReasonPortlessMissing
	default:
		return ReasonPortlessFailed
	}
}

// errors.Is(err, exec.ErrNotFound) is not enough: a missing path fails as fork/exec "no such file or directory", so a missing and a broken binary would otherwise share one warning.
func isMissingBinary(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "no such file or directory")
}

// Warn rides the existing Result.Warnings channel and every message ends by saying the service is still alive on its own port, so a missing route never reads as a crash.
func Warn(r Result) string {
	switch r.Reason {
	case "":
		return ""
	case ReasonPortlessMissing:
		return "no portless route published: the portless binary is not available in this environment; " +
			"the service is running on its own port as usual"
	case ReasonPortlessTimeout:
		return "portless did not respond in time and the route was not verified; " +
			"the service is running on its own port as usual"
	case ReasonPortlessFailed:
		return "the portless binary failed (an older Node is the usual cause) and no route was published; " +
			"the service is running on its own port as usual"
	case ReasonProxyNotRunning:
		return "portless has no proxy running, so no stable URL was published; " +
			"the service is running on its own port as usual"
	case ReasonProxyUnreachable:
		return "portless declares a proxy port that does not accept connections; " +
			"the service is running on its own port as usual"
	case ReasonRouteConflict:
		return fmt.Sprintf(
			"the portless route name %q is already taken by another port and was left untouched; "+
				"the service is running on its own port as usual", r.Host)
	case ReasonPortUnresolved:
		return "the service port is not resolved yet, so no route was published"
	case ReasonRouteNotServed:
		return "the portless proxy does not serve this route, so no URL was published; " +
			"the service is running on its own port as usual"
	default:
		return "no portless route published (" + r.Reason + "); " +
			"the service is running on its own port as usual"
	}
}

// routeState splits routed-but-dead from alive because a route whose backend is gone is exactly the footprint of a vroom that died without stopping its service.
type routeState int

const (
	routeUnknown routeState = iota
	routeRoutedDead
	routeAlive
)

// Reconcile is mandatory on every startup because portless prune never touches alias routes (pid: 0, counted as active, M5), so vroom is the only thing that can clean them; cleanup is fail-closed, removing only a provably own route and merely warning about one answering on another port, because nobody can prove it is someone else's; held is an Ownership rather than a bare port, since revocation deliberately keeps the handle for reconciliation, so a live handle does not mean we still own the name; and current is the whole candidate ladder, because a persisted name that is still a candidate this start may claim is not a rename — reading it as one would delete the route the start is about to reuse.

func (c *Client) Reconcile(prev string, held Ownership, current ...string) []string {
	if !c.HasBinary() || prev == "" || slices.Contains(current, prev) {
		return nil
	}

	published, st := c.liveRoute(prev)
	switch {
	case st == routeUnknown:
		// liveRoute returns published == 0 in this branch, so ownership is the only evidence left: without an unrevoked lease the route stays, because deleting someone else's route is the harm this all avoids.
		if !held.Owned {
			return []string{fmt.Sprintf(
				"a portless route named %q is no longer served and vroom no longer owns it; it was left untouched",
				Hostname(prev))}
		}
		_ = c.Remove(prev)
		return nil
	case published != held.Port:
		// It answers on a port we never persisted, so it is not ours and stays untouched; nobody can prove otherwise.
		return []string{fmt.Sprintf(
			"a portless route named %q is already serving another port (%d); it was left untouched",
			Hostname(prev), published)}
	case st == routeRoutedDead:
		if !held.Authorises(published) {
			return []string{fmt.Sprintf(
				"a portless route named %q is no longer served and vroom no longer owns it; it was left untouched",
				Hostname(prev))}
		}
		_ = c.Remove(prev)
		return nil
	default:
		return nil
	}
}

// liveRoute needs both signals: a response alone could be another name, and the stored port alone only proves we wrote (M2).
func (c *Client) liveRoute(name string) (int, routeState) {
	proxyPort, err := c.ProxyPort()
	if err != nil {
		return 0, routeUnknown
	}
	for _, scheme := range []string{"https", "http"} {
		status, err := c.probeWithTimeout(context.Background(), scheme, Hostname(name), proxyPort, probePath)
		if err != nil {
			continue
		}
		if status == 404 {
			continue
		}
		published, found, lookupErr := c.Lookup(name)
		if lookupErr != nil || !found {
			continue
		}
		if isBackendDown(status) {
			return published, routeRoutedDead
		}
		return published, routeAlive
	}
	return 0, routeUnknown
}

// isBackendDown counts only the proxy's own gateway errors: a 500 comes from the app itself, which is alive, and here the question is who is behind, not whether the proxy routes.
func isBackendDown(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusGatewayTimeout
}
