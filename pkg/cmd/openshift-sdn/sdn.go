package openshift_sdn

import (
	"fmt"
	"net"
	"strings"

	"github.com/golang/glog"

	kubeutilnet "k8s.io/apimachinery/pkg/util/net"
	kclientv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	kv1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	kexec "k8s.io/utils/exec"

	sdnnode "github.com/openshift/origin/pkg/network/node"
)

// initSDN sets up the sdn process.
func (sdn *OpenShiftSDN) initSDN() error {
	nodeName, nodeIP, err := sdn.resolveHost()
	if err != nil {
		return err
	}

	runtimeEndpoint := sdn.NodeConfig.DockerConfig.DockerShimSocket
	runtime, ok := sdn.NodeConfig.KubeletArguments["container-runtime"]
	if ok && len(runtime) == 1 && runtime[0] == "remote" {
		endpoint, ok := sdn.NodeConfig.KubeletArguments["container-runtime-endpoint"]
		if ok && len(endpoint) == 1 {
			runtimeEndpoint = endpoint[0]
		}
	}

	cniBinDir := "/opt/cni/bin"
	if val, ok := sdn.NodeConfig.KubeletArguments["cni-bin-dir"]; ok && len(val) == 1 {
		cniBinDir = val[0]
	}
	cniConfDir := "/etc/cni/net.d"
	if val, ok := sdn.NodeConfig.KubeletArguments["cni-conf-dir"]; ok && len(val) == 1 {
		cniConfDir = val[0]
	}

	// dockershim + kube CNI driver delegates hostport handling to plugins,
	// while CRI-O handles hostports itself. Thus we need to disable the
	// SDN's hostport handling when run under CRI-O.
	enableHostports := !strings.Contains(runtimeEndpoint, "crio")

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&kv1core.EventSinkImpl{Interface: sdn.informers.KubeClient.CoreV1().Events("")})
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, kclientv1.EventSource{Component: "openshift-sdn", Host: nodeName})

	sdn.OsdnNode, err = sdnnode.New(&sdnnode.OsdnNodeConfig{
		PluginName:         sdn.NodeConfig.NetworkConfig.NetworkPluginName,
		Hostname:           nodeName,
		SelfIP:             nodeIP,
		RuntimeEndpoint:    runtimeEndpoint,
		CNIBinDir:          cniBinDir,
		CNIConfDir:         cniConfDir,
		MTU:                sdn.NodeConfig.NetworkConfig.MTU,
		NetworkClient:      sdn.informers.NetworkClient,
		KClient:            sdn.informers.InternalClient,
		KubeInformers:      sdn.informers.InternalKubeInformers,
		NetworkInformers:   sdn.informers.NetworkInformers,
		IPTablesSyncPeriod: sdn.ProxyConfig.IPTables.SyncPeriod.Duration,
		MasqueradeBit:      sdn.ProxyConfig.IPTables.MasqueradeBit,
		ProxyMode:          sdn.ProxyConfig.Mode,
		EnableHostports:    enableHostports,
		Recorder:           eventRecorder,
	})
	return err
}

func (sdn *OpenShiftSDN) resolveHost() (string, string, error) {
	nodeName := sdn.NodeConfig.NodeName
	if nodeName == "" {
		output, err := kexec.New().Command("uname", "-n").CombinedOutput()
		if err != nil {
			return "", "", err
		}
		nodeName = strings.TrimSpace(string(output))
		glog.Infof("Resolved hostname to %q", nodeName)
	}

	// NodeIP must be a (non-localhost) IPv4 address that is configured on some local
	// network interface. We first try the configured NodeIP value, then parsing
	// NodeName as an IP address, then resolving NodeName in DNS, and then finally
	// just fall back to the IP of the default interface.
	nodeIP := sdn.NodeConfig.NodeIP
	if nodeIP != "" {
		ip := net.ParseIP(nodeIP)
		if ip == nil {
			return "", "", fmt.Errorf("configured NodeIP %q is not a valid IP address", nodeIP)
		} else if err := validIP(ip); err != nil {
			return "", "", fmt.Errorf("configured NodeIP %q is not valid: %v", nodeIP, err)
		}
	}
	if nodeIP == "" {
		ip := net.ParseIP(nodeName)
		if ip != nil {
			if err := validIP(ip); err != nil {
				glog.Warningf("Node name %q is not a valid node IP: %v", nodeName, err)
			} else {
				nodeIP = ip.String()
			}
		} else {
			addrs, err := net.LookupIP(nodeName)
			if err != nil {
				glog.Warningf("Failed to lookup IP address for node %q: %v", nodeName, err)
			} else {
				for _, addr := range addrs {
					if err := validIP(addr); err != nil {
						glog.V(5).Infof("Skipping resolved addr %q for node %q: %v", addr.String(), nodeName, err)
					} else {
						nodeIP = addr.String()
						break
					}
				}
			}
		}
		if nodeIP != "" {
			glog.Infof("Resolved IP address to %q", nodeIP)
		}
	}
	if nodeIP == "" {
		glog.Infof("Failed to determine node address from hostname; using default interface")
		defaultIP, err := kubeutilnet.ChooseHostInterface()
		if err != nil {
			return "", "", err
		}
		nodeIP = defaultIP.String()
	}

	return nodeName, nodeIP, nil
}

func validIP(ip net.IP) error {
	str := ip.String()

	if ip.IsLoopback() {
		return fmt.Errorf("cannot use loopback IP %q as node IP", str)
	}
	if ip.To4() == nil {
		return fmt.Errorf("IPv6 address %q not supported for node IP", str)
	}

	addrs, _ := net.InterfaceAddrs()
	found := false
	for _, addr := range addrs {
		if strings.HasPrefix(addr.String(), str+"/") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("node IP %q is not a local address", str)
	}

	return nil
}

// runSDN starts the sdn node process. Returns.
func (sdn *OpenShiftSDN) runSDN() error {
	return sdn.OsdnNode.Start()
}
