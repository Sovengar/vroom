# Summary: ci-integration-proxy

## Metadata
- **Completed:** 2026-10-11 15:45
- **Duration:** ~6 hours (6 review rounds + fix passes)
- **Plan Number:** 0001

## Scenarios
| Scenario (behavior.feature) | Status |
|-----------------------------|--------|
| A route the proxy picks up inside the window is published | ✅ Passed |
| A genuinely unserved route still degrades when the window closes | ✅ Passed |
| A declared proxy port that refuses connections degrades at once | ✅ Passed |
| A routed-but-dead backend still publishes its Url | ✅ Passed |
| A served route never pays the window | ✅ Passed |
| No proxy running degrades without probing | ✅ Passed |
| The window is on by default and can be opted out | ✅ Passed |
| A cancelled command stops the start and is not misreported | ✅ Passed |
| A cancelled start unwinds before the command exits | ✅ Passed |
| A cancelled commands.start.hooks.pre_run leaves no orphan | ✅ Passed |
| The ladder e2e Url answers from its own backend | ✅ Passed |
| A route survives a proxy restart | ✅ Passed |
| Live portless routes are not evicted | ✅ Passed |

## Commits
- fix(portless): handle route propagation window and cancellation
- chore(prototype): reproduce the CI integration proxy failure locally
- chore: add ci-integration-proxy plan and route-cancellation contract
- docs(adr): record the portless route propagation window
- chore(planning): track review rounds and fix passes
- fix(start): unwind a cancelled start cleanly and cut the post_run hook
- fix(cli): make Ctrl-C terminate vroom start

## Files
- Created: internal/cli/runstart_test.go, internal/logrun/logrun_cancel_unix_test.go, internal/logrun/logrun_unix.go, internal/logrun/logrun_windows.go, internal/startsvc/cancel_test.go, docs/planning/0001-fix-ci-integration-proxy/ (behavior.feature, context.md, issue.md, plan.md, prototype-brief.md, repro-ci-integration-proxy.sh, diagrams/)
- Modified: cmd/vroom/main.go, docs/FEATURES.md, docs/adr/adr-0013-vroom-registers-portless-routes.md, internal/cli/cli.go, internal/logrun/logrun.go, internal/portless/apply.go, internal/portless/apply_paths_test.go, internal/portless/ladder_e2e_test.go, internal/portless/portless.go, internal/portless/portless_test.go, internal/portless/reconcile_guard_test.go, internal/portless/warn_test.go, internal/process/dynamic_unix.go, internal/process/dynamic_unix_test.go, internal/startsvc/edges_test.go, internal/startsvc/helper_test.go, internal/startsvc/postrun_test.go, internal/startsvc/route_grant_test.go, internal/startsvc/route_ownership_test.go, internal/startsvc/route_test.go, internal/startsvc/startsvc.go, internal/tui/actions3_test.go, internal/tui/app.go, internal/tui/service_cmds_test.go

## Tests
- Added: 8 new test functions across cli, logrun, portless, startsvc
- System Tests: ✅ Passed (make test green, 100% coverage maintained)

## Documentation
- Changelog: ✅ Updated
- Docs: docs/FEATURES.md (propagation window + cancellation), ADR-0013 (measured-facts row + §6 correction), SKILL.md (bounded probe clause), behavior.feature (13 scenarios)
- ADR: ✅ Updated (ADR-0013 amended in place)

## Code Review Issues
- Critical Found: 0
- High Found: 0
- User Decision: Approved to continue

## Next Step
Merge to main (pending approval).
