package config

import (
	"fmt"
	"reflect"
	"sort"

	"dario.cat/mergo"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/spigell/pulumi-hcloud-kube-hetzner/internal/k8s/k8sconfig"
)

const (
	AgentRole  = "agent"
	ServerRole = "server"
	// ServerName must be a valid hostname.
	// Since ctx.Project() can be a quite long string, prefix for server name is 4 character.
	ServerNamePrefix = "phkh"
)

type Config struct {
	ctx *pulumi.Context

	// Nodepools is a map with agents and servers defined.
	// Required for at least one server node.
	// Default is not specified.
	Nodepools *NodepoolsConfig
	// Defaults is a map with default settings for agents and servers.
	// Global values for all nodes can be set here as well.
	// Can be empty, but required.
	// Default is not specified.
	Defaults *DefaultConfig
	// Network defines network configuration for cluster.
	// Can be empty, but required.
	// Default is not specified.
	Network *NetworkConfig
	// K8S defines a distribution-agnostic cluster configuration.
	// Can be empty, but required.
	// Default is not specified.
	K8S *k8sconfig.Config
}

// New returns the parsed configuration for the cluster as is without any modifications.
func New(ctx *pulumi.Context) *Config {
	var defaults *DefaultConfig
	var nodepools *NodepoolsConfig
	var network *NetworkConfig
	var k8s *k8sconfig.Config
	c := config.New(ctx, "")

	c.RequireSecretObject("defaults", &defaults)
	c.RequireSecretObject("nodepools", &nodepools)
	c.RequireSecretObject("network", &network)
	c.RequireSecretObject("k8s", &k8s)

	return &Config{
		ctx:       ctx,
		Nodepools: nodepools,
		Network:   network,
		Defaults:  defaults,
		K8S:       k8s,
	}
}

// WithInited returns the parsed configuration for the cluster with all the defaults set.
// Nodepools and Nodes returned sorted.
// This is required for the network module to work correctly when user changes order of nodepools and nodes.
func (c *Config) WithInited() *Config {
	c.Network.WithInited()
	c.Defaults.WithInited()
	c.K8S.WithInited()
	c.Nodepools.WithInited(c.ctx)
	c.Nodepools.SpecifyLeader()

	// Sort
	c.Nodepools.Agents = sortByID(c.Nodepools.Agents)
	c.Nodepools.Servers = sortByID(c.Nodepools.Servers)

	for i, pool := range c.Nodepools.Agents {
		c.Nodepools.Agents[i].Nodes = sortByID(pool.Nodes)
	}

	for i, pool := range c.Nodepools.Servers {
		c.Nodepools.Servers[i].Nodes = sortByID(pool.Nodes)
	}

	return c
}

// Nodes returns the nodes for the cluster.
// They are merged with the defaults and nodepool config values.
// They are sorted by majority as well.
func (c *Config) Nodes() ([]*NodeConfig, error) {
	nodes := make([]*NodeConfig, 0)

	for agentpoolIdx, agentpool := range c.Nodepools.Agents {
		if hetznerFirewallConfigured(agentpool.Config.Server) {
			c.Nodepools.Agents[agentpoolIdx].Config.Server.Firewall.Hetzner.MarkWithDedicatedPool()
		}
		for i, a := range agentpool.Nodes {
			a.Role = AgentRole
			if hetznerFirewallConfigured(a.Server) {
				c.Nodepools.Agents[agentpoolIdx].Nodes[i].Server.Firewall.Hetzner.MarkAsDedicated()
			}
			agent, err := merge(*a, agentpool.Config, *c.Defaults)
			if err != nil {
				return nil, fmt.Errorf("failed to merge the agent config: %w", err)
			}

			nodes = append(nodes, &agent)
		}
	}
	for serverpoolIdx, serverpool := range c.Nodepools.Servers {
		for i, s := range serverpool.Nodes {
			s.Role = ServerRole
			if hetznerFirewallConfigured(s.Server) {
				c.Nodepools.Servers[serverpoolIdx].Nodes[i].Server.Firewall.Hetzner.MarkAsDedicated()
			}
			s, err := merge(*s, serverpool.Config, *c.Defaults)
			if err != nil {
				return nil, fmt.Errorf("failed to merge server config: %w", err)
			}
			nodes = append(nodes, &s)
		}
	}

	return sortByMajority(nodes), nil
}

func (no *NodepoolsConfig) SpecifyLeader() {
	if len(no.Servers) == 1 && len(no.Servers[0].Nodes) == 1 {
		no.Servers[0].Nodes[0].Leader = true
	}
}

// sortByMajority sorts nodes by majority.
// The first if leader, then other servers, then workers.
func sortByMajority(n []*NodeConfig) []*NodeConfig {
	nodes := make([]*NodeConfig, 0)

	for _, node := range n {
		if node.Leader {
			nodes = append([]*NodeConfig{node}, nodes...)
			continue
		}
		if node.Role == ServerRole {
			nodes = append(nodes, node)
			continue
		}
	}

	for _, node := range n {
		if node.Role == AgentRole {
			nodes = append(nodes, node)
		}
	}

	return nodes
}

func sortByID[W WithID](unsorted []W) []W {
	sorted := make([]W, 0, len(unsorted))
	keys := make([]string, 0, len(unsorted))

	for _, k := range unsorted {
		keys = append(keys, k.GetID())
	}

	sort.Strings(keys)

	for i, k := range keys {
		sorted = append(sorted, unsorted[i])
		if sorted[i].GetID() != k {
			for _, v := range unsorted {
				if v.GetID() == k {
					sorted[i] = v
				}
			}
		}
	}

	return sorted
}

func merge(node NodeConfig, nodepool *NodeConfig, defaults DefaultConfig) (NodeConfig, error) {
	global := defaults.Global
	agents := defaults.Agents
	servers := defaults.Servers

	if nodepool == nil {
		nodepool = &NodeConfig{}
	}

	switch role := node.Role; role {
	case AgentRole:
		if err := mergo.Merge(agents, global, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
		if err := mergo.Merge(nodepool, agents, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
		if err := mergo.Merge(&node, nodepool, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
	case ServerRole:
		if err := mergo.Merge(servers, global, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
		if err := mergo.Merge(nodepool, servers, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
		if err := mergo.Merge(&node, nodepool, mergo.WithAppendSlice, mergo.WithTransformers(BoolTransformer{})); err != nil {
			return node, err
		}
	}
	return node, nil
}

func hetznerFirewallConfigured(server *ServerConfig) bool {
	if server != nil && server.Firewall != nil && server.Firewall.Hetzner != nil && server.Firewall.Hetzner.Enabled != nil && *server.Firewall.Hetzner.Enabled {
		return true
	}
	return false
}

// BoolTransformer is simple struct for mergo.
// ParameterDoc: none.
type BoolTransformer struct{}

// A Transformer for mergo to avoid overwriting false values from node level.
func (b BoolTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {
	if typ == reflect.TypeOf(new(bool)) { // Check for *bool type
		return func(dst, src reflect.Value) error {
			// If dst is nil, we should consider the src value
			if dst.IsNil() {
				dst.Set(src)
			}
			// If dst is set (even to false), do nothing
			return nil
		}
	}
	return nil
}
