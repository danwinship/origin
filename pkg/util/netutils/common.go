package netutils

import (
	"fmt"
	"net"

	kerrors "k8s.io/apimachinery/pkg/util/errors"
)

var localHosts []string = []string{"127.0.0.1", "::1", "localhost"}
var localSubnets []string = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7", "fe80::/10"}

// Generate the default gateway IP Address for a subnet
func GenerateDefaultGateway(sna *net.IPNet) net.IP {
	ip := sna.IP.To4()
	return net.IPv4(ip[0], ip[1], ip[2], ip[3]|0x1)
}

// Return Host IP Networks
// Ignores provided interfaces and filters loopback and non IPv4 addrs.
func GetHostIPNetworks(skipInterfaces []string) ([]*net.IPNet, []net.IP, error) {
	hostInterfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}

	skipInterfaceMap := make(map[string]bool)
	for _, ifaceName := range skipInterfaces {
		skipInterfaceMap[ifaceName] = true
	}

	errList := []error{}
	var hostIPNets []*net.IPNet
	var hostIPs []net.IP
	for _, iface := range hostInterfaces {
		if skipInterfaceMap[iface.Name] {
			continue
		}

		ifAddrs, err := iface.Addrs()
		if err != nil {
			errList = append(errList, err)
			continue
		}
		for _, addr := range ifAddrs {
			ip, ipNet, err := net.ParseCIDR(addr.String())
			if err != nil {
				errList = append(errList, err)
				continue
			}

			// Skip loopback and non IPv4 addrs
			if !ip.IsLoopback() && ip.To4() != nil {
				hostIPNets = append(hostIPNets, ipNet)
				hostIPs = append(hostIPs, ip)
			}
		}
	}
	return hostIPNets, hostIPs, kerrors.NewAggregate(errList)
}

// ParseCIDRMask parses a CIDR string and ensures that it has no bits set beyond the
// network mask length. Use this when the input is supposed to be either a description of
// a subnet (eg, "192.168.1.0/24", meaning "192.168.1.0 to 192.168.1.255"), or a mask for
// matching against (eg, "192.168.1.15/32", meaning "must match all 32 bits of the address
// "192.168.1.15"). Use net.ParseCIDR() when the input is a host address that also
// describes the subnet that it is on (eg, "192.168.1.15/24", meaning "the address
// 192.168.1.15 on the network 192.168.1.0/24").
func ParseCIDRMask(cidr string) (*net.IPNet, error) {
	ip, net, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	if !ip.Equal(net.IP) {
		maskLen, addrLen := net.Mask.Size()
		return nil, fmt.Errorf("CIDR network specification %q is not in canonical form (should be %s/%d or %s/%d?)", cidr, ip.Mask(net.Mask).String(), maskLen, ip.String(), addrLen)
	}
	return net, nil
}

// IsPrivateAddress returns true if given address in format "<host>[:<port>]" is a localhost or an ip from
// private network range (e.g. 172.30.0.1, 192.168.0.1).
func IsPrivateAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// assume indexName is of the form `host` without the port and go on.
		host = addr
	}
	for _, localHost := range localHosts {
		if host == localHost {
			return true
		}
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	for _, subnet := range localSubnets {
		ipnet, err := ParseCIDRMask(subnet)
		if err != nil {
			continue // should not happen
		}
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}
