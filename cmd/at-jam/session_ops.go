package main

import (
	"context"

	"github.com/aethons-tools/cove/internal/jam"
)

// sessionOps are the /app/user session header's actions over the supervisor
// (userui.SessionOps): teardown (release / restart / end) and a standing
// session's upgrade.
type sessionOps struct {
	store jam.Store
	sup   *jam.Supervisor
}

func (o sessionOps) Teardown(ctx context.Context, actorID string) error {
	return o.sup.Teardown(ctx, actorID)
}

func (o sessionOps) Upgrade(inst jam.Instance) (string, error) {
	res, err := jam.UpgradeStanding(o.store, o.sup, jam.ProjectName(o.store, inst.Project), inst.Role, inst.Name, false)
	if err != nil {
		return "", err
	}
	if res.State != "" {
		return res.State, nil
	}
	return res.Reason, nil
}

func (o sessionOps) UpgradeState(inst jam.Instance) string {
	return o.sup.StandingUpgradeState(jam.ProjectName(o.store, inst.Project), inst.Role, inst.Name)
}
