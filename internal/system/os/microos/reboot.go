package microos

import (
	"fmt"

	"github.com/spigell/pulumi-hcloud-kube-hetzner/internal/utils"
	"github.com/spigell/pulumi-hcloud-kube-hetzner/internal/utils/ssh/connection"

	"github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func (m *MicroOS) Reboot(ctx *pulumi.Context, con *connection.Connection) error {
	rebooted, err := remote.NewCommand(ctx, fmt.Sprintf("reboot-%s", m.ID), &remote.CommandArgs{
		Connection: con.RemoteCommand(),
		// Use very primitive way to reboot node.
		Create:   pulumi.String("(sleep 1 && sudo shutdown -r now) &"),
		Triggers: utils.ExtractRemoteCommandResources(m.resources),
	}, pulumi.DependsOn(m.resources))
	if err != nil {
		err = fmt.Errorf("error reboot node: %w", err)
		return err
	}

	m.resources = append(m.resources, rebooted)

	waited, err := local.NewCommand(ctx, fmt.Sprintf("local-wait-%s", m.ID), &local.CommandArgs{
		Create:   pulumi.Sprintf("sleep 120 && until nc -z %s 22; do sleep 5; done", con.IP),
		Triggers: utils.ExtractRemoteCommandResources(m.resources),
	}, pulumi.DependsOn([]pulumi.Resource{rebooted}),
		pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "10m"}),
	)
	if err != nil {
		err = fmt.Errorf("error waiting for node: %w", err)
		return err
	}
	m.resources = append(m.resources, waited)

	return nil
}
