package discovery

import (
	"context"
	"fmt"
	"github.com/grandcat/zeroconf"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
	"net"
)

func (discovery *DiscoveryCtrl) Register() {
	if discovery.Config.EnableAdvertisement {
		txtData, err := discovery.GetTxtData()
		if err != nil {
			klog.Error(err)
			return
		}
		txt, err := txtData.Encode()
		if err != nil {
			klog.Error(err)
			return
		}

		var ttl = discovery.Config.Ttl
		discovery.serverMux.Lock()
		discovery.mdnsServer, err = zeroconf.Register(fmt.Sprintf("%s_%s", discovery.Config.Name, discovery.ClusterId.GetClusterID()), discovery.Config.Service, discovery.Config.Domain, discovery.Config.Port, txt, discovery.getInterfaces(), ttl)
		if err != nil {
			discovery.serverMux.Unlock()
			klog.Error(err)
			return
		}
		discovery.mdnsServerAuth, err = zeroconf.Register(fmt.Sprintf("%s_%s", discovery.Config.Name, discovery.ClusterId.GetClusterID()), discovery.Config.AuthService, discovery.Config.Domain, 1234, nil, discovery.getInterfaces(), ttl)
		discovery.serverMux.Unlock()
		if err != nil {
			klog.Error(err)
			return
		}
		defer discovery.shutdownServer()
		<-discovery.stopMDNS
	}
}

func (discovery *DiscoveryCtrl) shutdownServer() {
	discovery.serverMux.Lock()
	defer discovery.serverMux.Unlock()
	discovery.mdnsServer.Shutdown()
	discovery.mdnsServerAuth.Shutdown()
}

func (discovery *DiscoveryCtrl) getInterfaces() []net.Interface {
	var interfaces []net.Interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	podNets, err := discovery.getPodNets()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		// select interfaces with IP addresses not in pod local network
		sel := false
		for _, addr := range addrs {
			ip := getIP(addr)
			if !isPod(podNets, ip) {
				if ip != nil && ip.To4() != nil {
					sel = true
				}
			}
		}
		if !sel {
			continue
		}

		if (ifi.Flags & net.FlagUp) == 0 {
			continue
		}
		if (ifi.Flags & net.FlagMulticast) > 0 {
			interfaces = append(interfaces, ifi)
		}
	}
	return interfaces
}

func (discovery *DiscoveryCtrl) getPodNets() ([]*net.IPNet, error) {
	nodes, err := discovery.crdClient.Client().CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	res := make([]*net.IPNet, 0, len(nodes.Items))
	for _, n := range nodes.Items {
		_, ipnet, err := net.ParseCIDR(n.Spec.PodCIDR)
		if err != nil {
			klog.Error(err, err.Error())
			continue
		}
		res = append(res, ipnet)
	}
	return res, nil
}

func isPod(podNets []*net.IPNet, ip net.IP) bool {
	for _, ipnet := range podNets {
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}
