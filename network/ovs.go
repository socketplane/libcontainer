// +build linux

package network

import (
	"fmt"
	"os/exec"

	"github.com/docker/libcontainer/utils"
)

// OVS is a network strategy that uses a bridge and creates
// an OVS internal port that is placed inside the container's
// namespace
type Ovs struct {
}

func (v *Ovs) Create(n *Network, nspid int, networkState *NetworkState) error {
	var (
		bridge = n.Bridge
		prefix = n.VethPrefix
	)
	if bridge == "" {
		return fmt.Errorf("bridge is not specified")
	}
	if prefix == "" {
		return fmt.Errorf("veth prefix is not specified")
	}
	name, err := createOvsInternalPort(prefix, bridge)
	if err != nil {
		return err
	}
	if err := SetMtu(name, n.Mtu); err != nil {
		return err
	}
	if err := InterfaceUp(name); err != nil {
		return err
	}
	if err := SetInterfaceInNamespacePid(name, nspid); err != nil {
		return err
	}
	networkState.OvsPort = name

	return nil
}

func (v *Ovs) Initialize(config *Network, networkState *NetworkState) error {
	var ovsPort = networkState.OvsPort
	if ovsPort == "" {
		return fmt.Errorf("ovsPort is not specified")
	}
	if err := InterfaceDown(ovsPort); err != nil {
		return fmt.Errorf("interface down %s %s", ovsPort, err)
	}
	if err := ChangeInterfaceName(ovsPort, defaultDevice); err != nil {
		return fmt.Errorf("change %s to %s %s", ovsPort, defaultDevice, err)
	}
	if config.MacAddress != "" {
		if err := SetInterfaceMac(defaultDevice, config.MacAddress); err != nil {
			return fmt.Errorf("set %s mac %s", defaultDevice, err)
		}
	}
	if err := SetInterfaceIp(defaultDevice, config.Address); err != nil {
		return fmt.Errorf("set %s ip %s", defaultDevice, err)
	}
	if config.IPv6Address != "" {
		if err := SetInterfaceIp(defaultDevice, config.IPv6Address); err != nil {
			return fmt.Errorf("set %s ipv6 %s", defaultDevice, err)
		}
	}

	if err := SetMtu(defaultDevice, config.Mtu); err != nil {
		return fmt.Errorf("set %s mtu to %d %s", defaultDevice, config.Mtu, err)
	}
	if err := InterfaceUp(defaultDevice); err != nil {
		return fmt.Errorf("%s up %s", defaultDevice, err)
	}
	if config.Gateway != "" {
		if err := SetDefaultGateway(config.Gateway, defaultDevice); err != nil {
			return fmt.Errorf("set gateway to %s on device %s failed with %s", config.Gateway, defaultDevice, err)
		}
	}
	if config.IPv6Gateway != "" {
		if err := SetDefaultGateway(config.IPv6Gateway, defaultDevice); err != nil {
			return fmt.Errorf("set gateway for ipv6 to %s on device %s failed with %s", config.IPv6Gateway, defaultDevice, err)
		}
	}
	return nil
}

// createOvsInternalPort will generate a random name for the
// the port and ensure that it has been created
func createOvsInternalPort(prefix string, bridge string) (name1 string, err error) {
	for i := 0; i < 10; i++ {
		if name1, err = utils.GenerateRandomName(prefix, 7); err != nil {
			return
		}

		// ToDo: Replace with a proper OVSDB-based call
        cmd := exec.Command("ovs-vsctl", "add-port", bridge, name1, "--", "set", "Interface", name1, "type=internal")
		if err = cmd.Run(); err != nil {
			return name1, fmt.Errorf("create ovs port %s on %s failed with %s", name1, bridge, err)
		}

		break
	}

	return
}
