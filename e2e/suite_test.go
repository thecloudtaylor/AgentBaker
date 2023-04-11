package e2e_test

import (
	"context"
	"fmt"
	"log"
	mrand "math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/agentbaker/pkg/agent"
	"github.com/Azure/agentbaker/pkg/agent/datamodel"
	"github.com/Azure/agentbakere2e/scenario"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/barkimedes/go-deepcopy"
)

func Test_All(t *testing.T) {
	r := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	ctx := context.Background()

	t.Parallel()

	suiteConfig, err := newSuiteConfig()
	if err != nil {
		log.Fatal(err)
	}

	if err := createE2ELoggingDir(); err != nil {
		log.Fatal(err)
	}

	scenarioTable := scenario.InitScenarioTable(suiteConfig.scenariosToRun)

	cloud, err := newAzureClient(suiteConfig.subscription)
	if err != nil {
		log.Fatal(err)
	}

	if err := ensureResourceGroup(ctx, t, cloud, suiteConfig.resourceGroupName); err != nil {
		log.Fatal(err)
	}

	clusters, err := listClusters(ctx, t, cloud, suiteConfig.resourceGroupName)
	if err != nil {
		log.Fatal(err)
	}

	paramCache := paramCache{}

	for _, scenario := range scenarioTable {
		scenario := scenario

		kube, cluster, clusterParams, subnetID := mustChooseCluster(ctx, t, r, cloud, suiteConfig, scenario, &clusters, paramCache)

		clusterName := *cluster.Name
		log.Printf("chose cluster: %q", clusterName)

		baseConfig, err := getBaseNodeBootstrappingConfiguration(ctx, t, cloud, suiteConfig, clusterParams)
		if err != nil {
			log.Fatal(err)
		}

		copied, err := deepcopy.Anything(baseConfig)
		if err != nil {
			log.Printf("In scenario %s, failed to copy base config: %s", scenario.Name, err)
			continue
		}
		nbc := copied.(*datamodel.NodeBootstrappingConfiguration)

		if scenario.Config.BootstrapConfigMutator != nil {
			scenario.Config.BootstrapConfigMutator(t, nbc)
		}

		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()

			caseLogsDir, err := createVMLogsDir(scenario.Name)
			if err != nil {
				log.Fatal(err)
			}

			opts := &scenarioRunOpts{
				cloud:         cloud,
				kube:          kube,
				suiteConfig:   suiteConfig,
				scenario:      scenario,
				chosenCluster: cluster,
				nbc:           nbc,
				subnetID:      subnetID,
				loggingDir:    caseLogsDir,
			}

			runScenario(ctx, t, r, opts)
		})
	}
}

func runScenario(ctx context.Context, t *testing.T, r *mrand.Rand, opts *scenarioRunOpts) {
	privateKeyBytes, publicKeyBytes, err := getNewRSAKeyPair(r)
	if err != nil {
		log.Println(err)
		return
	}

	vmssModel, cleanupVMSS, err := bootstrapVMSS(ctx, t, r, opts, publicKeyBytes)
	defer cleanupVMSS()
	isCSEError := isVMExtensionProvisioningError(err)
	vmssSucceeded := true
	if err != nil {
		vmssSucceeded = false
		if isCSEError {
			log.Printf("VM was unable to be provisioned due to a CSE error during scenario %s, will still atempt to extract provisioning logs... %v", opts.scenario.Name, err)
		} else {
			log.Fatal("Encountered an unknown error while creating VM:", err)
		}
		t.Log("VM was unable to be provisioned due to a CSE error, will still atempt to extract provisioning logs...")
	}

	if err := writeToFile(filepath.Join(caseLogsDir, "vmssId.txt"), *vmssModel.ID); err != nil {
		log.Fatal("failed to write vmss resource ID to disk", err)
	}

	// Perform posthoc log extraction when the VMSS creation succeeded or failed due to a CSE error
	if vmssSucceeded || isCSEError {
		debug := func() {
			err := pollExtractVMLogs(ctx, t, *vmssModel.Name, privateKeyBytes, opts)
			if err != nil {
				log.Fatal(err)
			}
		}
		defer debug()
	}

	// Only perform node readiness/pod-related checks when VMSS creation succeeded
	if vmssSucceeded {
		log.Println("vmss creation succeded, proceeding with node readiness and pod checks...")
		if err = validateNodeHealth(ctx, t, kube, *vmssModel.Name); err != nil {
			log.Fatal(err)
		}
		log.Println("node bootstrapping succeeded!")
	}
}

func bootstrapVMSS(ctx context.Context, t *testing.T, r *mrand.Rand, opts *scenarioRunOpts, publicKeyBytes []byte) (*armcompute.VirtualMachineScaleSet, func(), error) {
	nodeBootstrapping, err := getNodeBootstrapping(ctx, opts.nbc)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get node bootstrapping: %s", err)
	}

	vmssName := fmt.Sprintf("abtest%s", randomLowercaseString(r, 4))
	log.Printf("vmss name: %q", vmssName)

	cleanupVMSS := func() {
		log.Printf("deleting vmss %s", vmssName)
		poller, err := cloud.vmssClient.BeginDelete(ctx, *chosenCluster.Properties.NodeResourceGroup, vmssName, nil)
		if err != nil {
			// TODO - return error if failed and handle accordingly? We can't rely on t.Error() anymore
			log.Printf("error deleting vmss %q: %s", vmssName, err)
			return
		}
		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			// TODO - return error if failed and handle accordingly? We can't rely on t.Error() anymore
			log.Printf("error polling deleting vmss %q: %s", vmssName, err)
		}
		log.Printf("finished deleting vmss %q", vmssName)
	}

	vmssModel, err := createVMSSWithPayload(ctx, nodeBootstrapping.CustomData, nodeBootstrapping.CSE, vmssName, publicKeyBytes, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create VMSS with payload: %s", err)
	}

	return vmssModel, cleanupVMSS, nil
}

func getNodeBootstrapping(ctx context.Context, nbc *datamodel.NodeBootstrappingConfiguration) (*datamodel.NodeBootstrapping, error) {
	ab, err := agent.NewAgentBaker()
	if err != nil {
		return nil, err
	}
	nodeBootstrapping, err := ab.GetNodeBootstrapping(ctx, nbc)
	if err != nil {
		return nil, err
	}
	return nodeBootstrapping, nil
}

func validateNodeHealth(ctx context.Context, t *testing.T, kube *kubeclient, vmssName string) error {
	nodeName, err := waitUntilNodeReady(ctx, kube, vmssName)
	if err != nil {
		return fmt.Errorf("error waiting for node ready: %s", err)
	}

	err = ensureTestNginxPod(ctx, kube, nodeName)
	if err != nil {
		return fmt.Errorf("error waiting for pod ready: %s", err)
	}

	err = waitUntilPodDeleted(ctx, kube, nodeName)
	if err != nil {
		return fmt.Errorf("error waiting pod deleted: %s", err)
	}

	return nil
}
