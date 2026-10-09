package portless

import (
	"errors"
	"fmt"
)

// Retire drops the route this service is abandoning because the claim ladder moved it to a different name (a fallback upgraded to the stable one, or the stable one lost to another worktree). It differs from Reconcile on purpose: Reconcile may not delete a live route, but a name left behind by a ladder switch is live exactly in the fixed-port case and would otherwise outlive the handle — stop only revokes meta.RouteName, so the stale name would block the next worktree that falls back to it and would never be reconciled again.
// Fail-closed survives the stronger removal: it runs only while the ownership lease is unrevoked AND the entry still points at the port we persisted, so a route another actor overwrote (M8) is warned about and left untouched, never deleted.
func (c *Client) Retire(name string, held Ownership) []string {
	if !c.HasBinary() || name == "" || !held.Owned {
		return nil
	}
	published, found, err := c.Lookup(name)
	switch {
	case err != nil:
		return []string{fmt.Sprintf(
			"the old portless route %q could not be read back, so it was left untouched",
			Hostname(name))}
	case !found:
		// Already gone: Reconcile removed it, or the stop that revoked ownership also removed it. Not an error, and not worth a warning.
		return nil
	case published != held.Port:
		return []string{fmt.Sprintf(
			"the old portless route %q is now serving another port (%d); it was left untouched",
			Hostname(name), published)}
	}
	// RemoveAbsent, not Remove: Remove folds every exit 1 into the benign "no alias found" (its contract, §adr-0013/17), which would report a retirement that never happened as a success and lose the route silently.
	if err := c.RemoveAbsent(name); err != nil && !errors.Is(err, ErrRouteAbsent) {
		return []string{fmt.Sprintf(
			"the old portless route %q could not be removed and was left behind (%v)",
			Hostname(name), err)}
	}
	return nil
}
