package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	clientset "k8s.io/client-go/kubernetes"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	operatorclient "github.com/openshift/client-go/operator/clientset/versioned"
	"github.com/openshift/origin/test/extended/util/azure"
)

type ClusterConfiguration struct {
	ProviderName string `json:"type"`

	// These fields chosen to match the e2e configuration we fill
	ProjectID   string
	Region      string
	Zone        string
	NumNodes    int
	MultiMaster bool
	MultiZone   bool
	Zones       []string
	ConfigFile  string

	NetworkPluginIDs []string
}

func (c *ClusterConfiguration) ToJSONString() string {
	out, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// DiscoverConfig uses the cluster to setup the cloud provider config.
func DiscoverConfig() (*ClusterConfiguration, error) {
	p, masters, nonMasters, networkSpec := testPlatformStatus, testMasters, testNonMasters, testNetworkSpec
	if p == nil {
		clientConfig, err := e2e.LoadConfig(true)
		if err != nil {
			return nil, err
		}

		coreClient, err := clientset.NewForConfig(clientConfig)
		if err != nil {
			return nil, err
		}
		configClient, err := configclient.NewForConfig(clientConfig)
		if err != nil {
			return nil, err
		}
		operatorClient, err := operatorclient.NewForConfig(clientConfig)
		if err != nil {
			return nil, err
		}

		infra, err := configClient.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		p = infra.Status.PlatformStatus
		if p == nil {
			return nil, fmt.Errorf("status.platformStatus must be set")
		}

		masters, err = coreClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
			LabelSelector: "node-role.kubernetes.io/master=",
		})
		if err != nil {
			return nil, err
		}

		nonMasters, err = coreClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
			LabelSelector: "!node-role.kubernetes.io/master",
		})
		if err != nil {
			return nil, err
		}

		networkConfig, err := operatorClient.OperatorV1().Networks().Get(context.Background(), "cluster", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		networkSpec = &networkConfig.Spec
	}

	var networkPluginIDs []string
	networkPluginIDs = append(networkPluginIDs, string(networkSpec.DefaultNetwork.Type))
	if networkSpec.DefaultNetwork.OpenShiftSDNConfig != nil && networkSpec.DefaultNetwork.OpenShiftSDNConfig.Mode != "" {
		networkPluginIDs = append(networkPluginIDs, string(networkSpec.DefaultNetwork.Type)+"/"+string(networkSpec.DefaultNetwork.OpenShiftSDNConfig.Mode))
	}

	if p.Type == configv1.NonePlatformType {
		return &ClusterConfiguration{
			NetworkPluginIDs: networkPluginIDs,
		}, nil
	}

	zones := sets.NewString()
	for _, node := range masters.Items {
		zones.Insert(node.Labels["failure-domain.beta.kubernetes.io/zone"])
	}
	zones.Delete("")

	config := &ClusterConfiguration{
		MultiMaster:      len(masters.Items) > 1,
		MultiZone:        zones.Len() > 1,
		Zones:            zones.List(),
		NetworkPluginIDs: networkPluginIDs,
	}
	if zones.Len() > 0 {
		config.Zone = zones.List()[0]
	}
	if len(nonMasters.Items) == 0 {
		config.NumNodes = len(nonMasters.Items)
	} else {
		config.NumNodes = len(masters.Items)
	}

	switch {
	case p.AWS != nil:
		config.ProviderName = "aws"
		config.Region = p.AWS.Region

	case p.GCP != nil:
		config.ProviderName = "gce"
		config.ProjectID = p.GCP.ProjectID
		config.Region = p.GCP.Region

	case p.Azure != nil:
		config.ProviderName = "azure"

		data, err := azure.LoadConfigFile()
		if err != nil {
			return nil, err
		}
		tmpFile, err := ioutil.TempFile("", "e2e-*")
		if err != nil {
			return nil, err
		}
		tmpFile.Close()
		if err := ioutil.WriteFile(tmpFile.Name(), data, 0600); err != nil {
			return nil, err
		}
		config.ConfigFile = tmpFile.Name()
	}

	return config, nil
}

var testPlatformStatus *configv1.PlatformStatus
var testMasters, testNonMasters *corev1.NodeList
var testNetworkSpec *operatorv1.NetworkSpec

// SetTestConfig sets the configuration that will be "discovered" by DiscoverConfig
func SetTestConfig(platformStatus *configv1.PlatformStatus, masters, nonMasters *corev1.NodeList, networkSpec *operatorv1.NetworkSpec) {
	testPlatformStatus = platformStatus
	testMasters = masters
	testNonMasters = nonMasters
	testNetworkSpec = networkSpec
}

// MatchFn returns a function that tests if a named function should be run based on
// the cluster configuration
func (c *ClusterConfiguration) MatchFn() func(string) bool {
	var skips []string
	skips = append(skips, fmt.Sprintf("[Skipped:%s]", c.ProviderName))
	for _, id := range c.NetworkPluginIDs {
		skips = append(skips, fmt.Sprintf("[Skipped:Network/%s]", id))
	}
	matchFn := func(name string) bool {
		for _, skip := range skips {
			if strings.Contains(name, skip) {
				return false
			}
		}
		return true
	}
	return matchFn
}
