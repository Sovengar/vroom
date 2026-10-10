# Expected behavior for `fix/ci-integration-proxy` (change under test: commit 7c1ab05).
# Source of truth for behavior — for user verification only; NOT wired to any runner.
# The executor turns each scenario into real unit/integration tests.

Feature: Apply waits out the portless route propagation window

  portless exposes a freshly written alias to its proxy asynchronously: through
  the fs.watch debounce (~100 ms) when the watcher works, or through a 3 s
  polling fallback when it does not (the inotify-starved CI runner). Before the
  fix, verify() probed exactly once, read the stale cache and degraded a route
  the proxy was about to serve — the deterministic CI failure: three integration
  tests ended {degraded, route_not_served, empty Url} instead of publishing.
  After the fix, Apply retries the probe for a bounded window
  (DefaultVerifyWait 3.5 s, 250 ms poll) and only then degrades.

  Scenario: A route the proxy picks up inside the window is published
    Given a proxy that answers 404 for a freshly written route and starts serving it
      a few probes later, inside the propagation window
    When vroom applies the route
    Then the result is "registered" with a non-empty Url
    And the stale 404 was retried, not taken as final

  Scenario: A genuinely unserved route still degrades when the window closes
    Given a proxy that keeps answering 404 for the route for the whole window
    When the propagation window closes
    Then the result is "degraded" with reason "route_not_served" and no Url
    # Degradation semantics unchanged; only the moment moves past the window.

  Scenario: A declared proxy port that refuses connections degrades at once
    Given portless declares a proxy port that accepts no TCP connections
    When vroom applies the route
    Then the result is "degraded" with reason "proxy_unreachable"
    And the write is not undone (Registered stays true)
    And the bounded window is not paid (a port that cannot serve never will)

  Scenario: A routed-but-dead backend still publishes its Url
    Given the proxy answers 502 for the route (routing proven, backend down)
    When vroom applies the route
    Then the result is "registered" with the route Url

  Scenario: A served route never pays the window
    Given a proxy that serves the route on a probe
    When vroom applies the route
    Then the Url is published on that probe without waiting for the window to close

  Scenario: No proxy running degrades without probing
    Given portless reports no proxy port
    When vroom applies the route
    Then the result is "degraded" with reason "proxy_not_running", Registered true and no Url

  Scenario: The window is on by default and can be opted out
    Given a client built with New()
    Then the propagation window defaults to 3.5 s with a 250 ms poll interval
    And WithVerifyWait(0) restores the single immediate probe the deterministic fakes pin

  Scenario: A cancelled command stops the start and is not misreported
    Given a command context that is cancelled while the propagation window is open
    When vroom applies the route
    Then Apply returns promptly, well before the window closes
    And the result is "degraded" with reason "cancelled" and no Url
    And the route is not misreported as "route_not_served"

  Scenario: The ladder e2e Url answers from its own backend
    Given a stable Url registered through a real portless proxy for a worktree
    When a client opens the Url
    Then within a bounded wait it answers HTTP 200 with that worktree's backend body,
      never the previous worktree's backend

  Scenario: A route survives a proxy restart
    Given a route registered through an isolated real proxy
    When the proxy is killed and restarted
    Then the route is still registered to the same port and the Url is served again

  Scenario: Live portless routes are not evicted
    Given a live route owned by a running app
    When vroom applies its own route and later releases it
    Then the live route keeps serving its own backend throughout
