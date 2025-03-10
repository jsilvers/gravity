/*
Copyright 2018 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gravitational/gravity/lib/defaults"
	"github.com/gravitational/gravity/lib/httplib"
	"github.com/gravitational/gravity/lib/install"
	"github.com/gravitational/gravity/lib/localenv"
	"github.com/gravitational/gravity/lib/ops"
	"github.com/gravitational/gravity/lib/pack/webpack"
	"github.com/gravitational/gravity/lib/processconfig"
	rpcserver "github.com/gravitational/gravity/lib/rpc/server"
	"github.com/gravitational/gravity/lib/state"
	"github.com/gravitational/gravity/lib/storage"
	"github.com/gravitational/gravity/lib/systeminfo"
	"github.com/gravitational/gravity/lib/utils"
	"github.com/gravitational/gravity/tool/common"

	"github.com/gravitational/roundtrip"
	"github.com/gravitational/trace"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

// LocalEnvironmentFactory defines an interface for creating operation-specific environments
type LocalEnvironmentFactory interface {
	// NewLocalEnv creates a new default environment.
	// It will use the location pointer file to find the location of the custom state
	// directory if available and will fall back to defaults.GravityDir otherwise.
	// All other environments are located under this common root directory
	NewLocalEnv() (*localenv.LocalEnvironment, error)
	// TODO(dmitri): generalize operation environment under a single
	// NewOperationEnv API
	// NewUpdateEnv creates a new environment for update operations
	NewUpdateEnv() (*localenv.LocalEnvironment, error)
	// NewJoinEnv creates a new environment for join operations
	NewJoinEnv() (*localenv.LocalEnvironment, error)
}

// NewLocalEnv returns an instance of the local environment.
func (g *Application) NewLocalEnv() (env *localenv.LocalEnvironment, err error) {
	localStateDir, err := getLocalStateDir(*g.StateDir)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return g.getEnv(localStateDir)
}

// NewInstallEnv returns an instance of the local environment for commands that
// initialize cluster environment (i.e. install or join).
func (g *Application) NewInstallEnv() (env *localenv.LocalEnvironment, err error) {
	stateDir := *g.StateDir
	if stateDir == "" {
		stateDir = defaults.LocalGravityDir
	} else {
		stateDir = filepath.Join(stateDir, defaults.LocalDir)
	}
	return g.getEnv(stateDir)
}

// NewUpdateEnv returns an instance of the local environment that is used
// only for updates
func (g *Application) NewUpdateEnv() (*localenv.LocalEnvironment, error) {
	dir, err := state.GetStateDir()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return g.getEnv(state.GravityUpdateDir(dir))
}

// NewJoinEnv returns an instance of local environment where join-specific data is stored
func (g *Application) NewJoinEnv() (*localenv.LocalEnvironment, error) {
	const failImmediatelyIfLocked = -1
	stateDir, err := state.GravityInstallDir()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	err = os.MkdirAll(stateDir, defaults.SharedDirMask)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}
	return g.getEnvWithArgs(localenv.LocalEnvironmentArgs{
		StateDir:         stateDir,
		Insecure:         *g.Insecure,
		Silent:           localenv.Silent(*g.Silent),
		Debug:            *g.Debug,
		EtcdRetryTimeout: *g.EtcdRetryTimeout,
		BoltOpenTimeout:  failImmediatelyIfLocked,
		Reporter:         common.ProgressReporter(*g.Silent),
	})
}

func (g *Application) getEnv(stateDir string) (*localenv.LocalEnvironment, error) {
	return g.getEnvWithArgs(localenv.LocalEnvironmentArgs{
		StateDir:         stateDir,
		Insecure:         *g.Insecure,
		Silent:           localenv.Silent(*g.Silent),
		Debug:            *g.Debug,
		EtcdRetryTimeout: *g.EtcdRetryTimeout,
		Reporter:         common.ProgressReporter(*g.Silent),
	})
}

func (g *Application) getEnvWithArgs(args localenv.LocalEnvironmentArgs) (*localenv.LocalEnvironment, error) {
	if *g.StateDir != defaults.LocalGravityDir {
		args.LocalKeyStoreDir = *g.StateDir
	}
	// set insecure in devmode so we won't need to use
	// --insecure flag all the time
	cfg, _, err := processconfig.ReadConfig("")
	if err == nil && cfg.Devmode {
		args.Insecure = true
	}
	return localenv.NewLocalEnvironment(args)
}

// isUpdateCommand returns true if the specified command is
// an upgrade related command
func (g *Application) isUpdateCommand(cmd string) bool {
	switch cmd {
	case g.PlanCmd.FullCommand(),
		g.PlanDisplayCmd.FullCommand(),
		g.PlanExecuteCmd.FullCommand(),
		g.PlanRollbackCmd.FullCommand(),
		g.PlanResumeCmd.FullCommand(),
		g.PlanCompleteCmd.FullCommand(),
		g.UpdatePlanInitCmd.FullCommand(),
		g.UpdateTriggerCmd.FullCommand(),
		g.UpgradeCmd.FullCommand():
		return true
	case g.RPCAgentRunCmd.FullCommand():
		return len(*g.RPCAgentRunCmd.Args) > 0
	case g.RPCAgentDeployCmd.FullCommand():
		return len(*g.RPCAgentDeployCmd.LeaderArgs) > 0 ||
			len(*g.RPCAgentDeployCmd.NodeArgs) > 0
	}
	return false
}

// isExpandCommand returns true if the specified command is
// expand-related command
func (g *Application) isExpandCommand(cmd string) bool {
	switch cmd {
	case g.AutoJoinCmd.FullCommand(),
		g.PlanCmd.FullCommand(),
		g.PlanDisplayCmd.FullCommand(),
		g.PlanExecuteCmd.FullCommand(),
		g.PlanRollbackCmd.FullCommand(),
		g.PlanCompleteCmd.FullCommand(),
		g.PlanResumeCmd.FullCommand():
		return true
	}
	return false
}

// ConfigureNoProxy configures the current process to not use any configured HTTP proxy when connecting to any
// destination by IP address, or a domain with a suffix of .local. Gravity internally connects to nodes by IP address,
// and by queries to kubernetes using the .local suffix. The side effect is, connections towards the internet by IP
// address and not a configured domain name will not be able to invoke a proxy. This should be a reasonable tradeoff,
// because with a cluster that changes over time, it's difficult for us to accuratly detect what IP addresses need to
// have no_proxy set.
func ConfigureNoProxy() {
	// The golang HTTP proxy env variable detection only uses the first detected http proxy env variable
	// so we need to grab both to make sure we edit the correct one.
	// https://github.com/golang/net/blob/c21de06aaf072cea07f3a65d6970e5c7d8b6cd6d/http/httpproxy/proxy.go#L91-L107
	proxy := map[string]string{
		"NO_PROXY": os.Getenv("NO_PROXY"),
		"no_proxy": os.Getenv("no_proxy"),
	}

	for k, v := range proxy {
		if len(v) != 0 {
			os.Setenv(k, strings.Join([]string{v, "0.0.0.0/0", ".local"}, ","))
			return
		}
	}

	os.Setenv("NO_PROXY", strings.Join([]string{"0.0.0.0/0", ".local"}, ","))
}

func getLocalStateDir(stateDir string) (localStateDir string, err error) {
	if stateDir != "" {
		// If state directory has been explicitly specified on command line,
		// use it
		return stateDir, nil
	}
	stateDir, err = state.GetStateDir()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return filepath.Join(stateDir, defaults.LocalDir), nil
}

// findServer searches the provided cluster's state for a server that matches one of the provided
// tokens, where a token can be the server's advertise IP, hostname or AWS internal DNS name
func findServer(site ops.Site, tokens []string) (*storage.Server, error) {
	for _, server := range site.ClusterState.Servers {
		for _, token := range tokens {
			if token == "" {
				continue
			}
			switch token {
			case server.AdvertiseIP, server.Hostname, server.Nodename:
				return &server, nil
			}
		}
	}
	return nil, trace.NotFound("could not find server matching %v among registered cluster nodes",
		tokens)
}

// findLocalServer searches the provided cluster's state for the server that matches the one
// the current command is being executed from
func findLocalServer(site ops.Site) (*storage.Server, error) {
	// collect the machines's IP addresses and search by them
	ifaces, err := systeminfo.NetworkInterfaces()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if len(ifaces) == 0 {
		return nil, trace.NotFound("no network interfaces found")
	}

	var ips []string
	for _, iface := range ifaces {
		ips = append(ips, iface.IPv4)
	}

	server, err := findServer(site, ips)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return server, nil
}

func isCancelledError(err error) bool {
	if err == nil {
		return false
	}
	return trace.IsCompareFailed(err) && strings.Contains(err.Error(), "cancelled")
}

func watchReconnects(ctx context.Context, cancel context.CancelFunc, watchCh <-chan rpcserver.WatchEvent) {
	go func() {
		for event := range watchCh {
			if event.Error == nil {
				continue
			}
			log.Warnf("Failed to reconnect to %v: %v.", event.Peer, event.Error)
			cancel()
			return
		}
	}()
}

func loadRPCCredentials(ctx context.Context, addr, token string) (*rpcserver.Credentials, error) {
	// Assume addr to be a complete address if it's prefixed with `http`
	if !strings.Contains(addr, "http") {
		host, port := utils.SplitHostPort(addr, strconv.Itoa(defaults.GravitySiteNodePort))
		addr = fmt.Sprintf("https://%v:%v", host, port)
	}
	httpClient := roundtrip.HTTPClient(httplib.GetClient(true))
	packages, err := webpack.NewBearerClient(addr, token, httpClient)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	creds, err := install.LoadRPCCredentials(ctx, packages)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return creds, nil
}

// updateCommandWithFlags returns new command line for the specified command.
// flagsToAdd are added to the resulting command line if not yet present.
//
// The resulting command line adheres to command line format accepted by systemd.
// See https://www.freedesktop.org/software/systemd/man/systemd.service.html#Command%20lines for details
func updateCommandWithFlags(command []string, parser ArgsParser, flagsToAdd []flag) (args []string, err error) {
	ctx, err := parser.ParseArgs(command)
	if err != nil {
		log.WithError(err).Warn("Failed to parse command line.")
		return nil, trace.Wrap(err)
	}
	outputCommand := ctx.SelectedCommand.FullCommand()
	for _, el := range ctx.Elements {
		switch c := el.Clause.(type) {
		case *kingpin.ArgClause:
			args = append(args, strconv.Quote(*el.Value))
		case *kingpin.FlagClause:
			if _, ok := c.Model().Value.(boolFlag); ok {
				args = append(args, fmt.Sprint("--", c.Model().Name))
			} else {
				args = append(args, fmt.Sprint("--", c.Model().Name), strconv.Quote(*el.Value))
			}
			for i, flag := range flagsToAdd {
				model := c.Model()
				if model.Name == flag.name {
					flagsToAdd = append(flagsToAdd[:i], flagsToAdd[i+1:]...)
				}
			}
		}
	}
	for _, flag := range flagsToAdd {
		args = append(args, fmt.Sprint("--", flag.name), strconv.Quote(flag.value))
	}
	return append([]string{outputCommand}, args...), nil
}

type flag struct {
	name  string
	value string
}

type boolFlag interface {
	// IsBoolFlag returns true to indicate a boolean flag
	IsBoolFlag() bool
}

func parseArgs(args []string) (*kingpin.ParseContext, error) {
	app := kingpin.New("gravity", "")
	return RegisterCommands(app).ParseContext(args)
}

// ParseArgs parses the specified command line arguments into a parse context
func (r ArgsParserFunc) ParseArgs(args []string) (*kingpin.ParseContext, error) {
	return r(args)
}

// ArgsParserFunc is a functional wrapper for ArgsParser to enable ordinary functions
// as ArgsParsers
type ArgsParserFunc func(args []string) (*kingpin.ParseContext, error)

// ArgsParser parses Gravity command line arguments
type ArgsParser interface {
	// ParseArgs parses the specified command line arguments into a parse context
	ParseArgs(args []string) (*kingpin.ParseContext, error)
}
