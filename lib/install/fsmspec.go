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

package install

import (
	"strings"

	"github.com/gravitational/gravity/lib/fsm"
	"github.com/gravitational/gravity/lib/httplib"
	"github.com/gravitational/gravity/lib/install/phases"
	"github.com/gravitational/gravity/lib/ops/resources/gravity"
	"github.com/gravitational/gravity/lib/schema"

	"github.com/gravitational/trace"
	"k8s.io/client-go/kubernetes"
)

// FSMSpec returns a function that returns an appropriate phase executor
// based on the provided params
func FSMSpec(config FSMConfig) fsm.FSMSpecFunc {
	return func(p fsm.ExecutorParams, remote fsm.Remote) (fsm.PhaseExecutor, error) {
		switch {
		case strings.HasPrefix(p.Phase.ID, phases.InitPhase):
			return phases.NewInit(p,
				config.Operator,
				config.Apps,
				config.Packages)

		case p.Phase.ID == phases.ChecksPhase:
			return phases.NewChecks(p,
				config.Operator,
				config.OperationKey)

		case p.Phase.ID == phases.ConfigurePhase:
			return phases.NewConfigure(p,
				config.Operator)

		case strings.HasPrefix(p.Phase.ID, phases.BootstrapPhase):
			return phases.NewBootstrap(p,
				config.Operator,
				config.Apps,
				config.LocalBackend, remote)

		case strings.HasPrefix(p.Phase.ID, phases.PullPhase):
			return phases.NewPull(p,
				config.Operator,
				config.Packages,
				config.LocalPackages,
				config.Apps,
				config.LocalApps, remote)

		case strings.HasPrefix(p.Phase.ID, phases.MastersPhase), strings.HasPrefix(p.Phase.ID, phases.NodesPhase):
			return phases.NewSystem(p,
				config.Operator, remote)

		case p.Phase.ID == phases.WaitPhase:
			client, err := getKubeClient(p)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return phases.NewWait(p,
				config.Operator,
				client)

		case p.Phase.ID == phases.HealthPhase:
			return phases.NewHealth(p,
				config.Operator)

		case p.Phase.ID == phases.RBACPhase:
			client, err := getKubeClient(p)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return phases.NewRBAC(p,
				config.Operator,
				config.LocalApps,
				client)

		case p.Phase.ID == phases.CorednsPhase:
			client, err := getKubeClient(p)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return phases.NewCorednsPhase(p,
				config.Operator,
				client)

		case p.Phase.ID == phases.SystemResourcesPhase:
			client, err := getKubeClient(p)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return phases.NewSystemResources(p,
				config.Operator,
				client)

		case p.Phase.ID == phases.UserResourcesPhase:
			return phases.NewUserResources(p,
				config.Operator)

		case strings.HasPrefix(p.Phase.ID, phases.ExportPhase):
			return phases.NewExport(p,
				config.Operator,
				config.LocalPackages,
				config.LocalApps, remote)

		case strings.HasPrefix(p.Phase.ID, phases.RuntimePhase), strings.HasPrefix(p.Phase.ID, phases.AppPhase):
			return phases.NewApp(p,
				config.Operator,
				config.LocalApps)

		case p.Phase.ID == phases.ConnectInstallerPhase:
			return phases.NewConnectInstaller(p,
				config.Operator)

		case strings.HasPrefix(p.Phase.ID, phases.EnableElectionPhase):
			return phases.NewEnableElectionPhase(p, config.Operator)

		case strings.HasPrefix(p.Phase.ID, phases.InstallOverlayPhase):
			return phases.NewHook(p,
				config.Operator,
				config.LocalApps,
				schema.HookNetworkInstall)

		case strings.HasPrefix(p.Phase.ID, phases.GravityResourcesPhase):
			operator, err := config.LocalClusterClient()
			if err != nil {
				return nil, trace.Wrap(err)
			}
			factory, err := gravity.New(gravity.Config{
				Operator: operator,
			})
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return phases.NewGravityResourcesPhase(p, operator, factory)

		default:
			return nil, trace.BadParameter("unknown phase %q", p.Phase.ID)
		}
	}
}

func getKubeClient(p fsm.ExecutorParams) (*kubernetes.Clientset, error) {
	client, _, err := httplib.GetClusterKubeClient(p.Plan.DNSConfig.Addr())
	return client, trace.Wrap(err)
}
