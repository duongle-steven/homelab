package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// errUnreachable: ssh could not reach the node, so another may answer
var errUnreachable = errors.New("unreachable")

// Startup runs a node's cluster-startup (profiles/cluster-startup.nix) by hand and waits for it. The nodes run it themselves
// at boot; this is for a shutdown that failed with the nodes up, or to watch it finish.
func Startup(ctx context.Context, environment string, progress io.Writer) error {
	cfg, err := loadConfig(environment)
	if err != nil {
		return err
	}
	c := commands{progress: progress}
	return startup(append([]host{cfg.seed}, cfg.joiners...), func(node host) error {
		err := c.run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+node.address.Addr().String(), "cluster-startup --force")
		// ssh's own failure, not the command's
		if err != nil && strings.Contains(err.Error(), "exit status 255") {
			return fmt.Errorf("%w: %s: %w", errUnreachable, node.name, err)
		}
		return err
	})
}

func startup(nodes []host, run func(host) error) error {
	for _, node := range nodes {
		err := run(node)
		if errors.Is(err, errUnreachable) {
			continue
		}
		return err
	}
	return errors.New("no node answered")
}
