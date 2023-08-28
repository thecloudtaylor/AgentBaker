package scenario

import (
	"github.com/Azure/agentbaker/pkg/agent/datamodel"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

// NOTE: this works only if adding a VMSS to an existing cluster created with:
// az aks create -g <rg> -n <name> --network-plugin=kubenet --network-policy=calico
func calico() *Scenario {
	return &Scenario{
		Name:        "calico",
		Description: "Test an Ubuntu 22.04 node configured for Calico NPM",
		// This is the same as ubuntu2204, except use a larger VM SKU and set netpol Calico.
		Config: Config{
			ClusterSelector: NetworkPluginKubenetSelector,
			ClusterMutator:  NetworkPluginKubenetMutator,
			BootstrapConfigMutator: func(nbc *datamodel.NodeBootstrappingConfiguration) {
				nbc.ContainerService.Properties.AgentPoolProfiles[0].Distro = "aks-ubuntu-containerd-22.04-gen2"
				nbc.AgentPoolProfile.Distro = "aks-ubuntu-containerd-22.04-gen2"
				nbc.ContainerService.Properties.OrchestratorProfile.KubernetesConfig.NetworkPolicy = "calico"
				nbc.AgentPoolProfile.KubernetesConfig.NetworkPolicy = "calico"
			},
			VMConfigMutator: func(vmss *armcompute.VirtualMachineScaleSet) {
				vmss.Properties.VirtualMachineProfile.StorageProfile.ImageReference = &armcompute.ImageReference{
					ID: to.Ptr(DefaultImageVersionIDs["ubuntu2204"]),
				}
				vmss.SKU.Name = to.Ptr("Standard_D4s_v3")
			},
		},
	}
}
