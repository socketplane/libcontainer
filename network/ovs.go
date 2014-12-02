// +build linux

package network

import (
	"fmt"
	"reflect"
	"time"

	"github.com/docker/libcontainer/utils"
	"github.com/socketplane/libovsdb"
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
	// Add a dummy sleep to make sure the interface is seen by the subsequent calls.
	time.Sleep(time.Second * 1)
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
	if name1, err = utils.GenerateRandomName(prefix, 7); err != nil {
		return
	}

	ovs, err := ovs_connect()
	if err == nil {
		addInternalPort(ovs, bridge, name1)
	}
	return
}

var update chan *libovsdb.TableUpdates
var cache map[string]map[string]libovsdb.Row

func addInternalPort(ovs *libovsdb.OvsdbClient, bridgeName string, portName string) {
	namedPortUuid := "port"
	namedIntfUuid := "intf"

	// intf row to insert
	intf := make(map[string]interface{})
	intf["name"] = portName
	intf["type"] = `internal`

	insertIntfOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Interface",
		Row:      intf,
		UUIDName: namedIntfUuid,
	}

	// port row to insert
	port := make(map[string]interface{})
	port["name"] = portName
	port["interfaces"] = libovsdb.UUID{namedIntfUuid}

	insertPortOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Port",
		Row:      port,
		UUIDName: namedPortUuid,
	}

	// Inserting a row in Port table requires mutating the bridge table.
	mutateUuid := []libovsdb.UUID{libovsdb.UUID{namedPortUuid}}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUuid)
	mutation := libovsdb.NewMutation("ports", "insert", mutateSet)
	condition := libovsdb.NewCondition("name", "==", bridgeName)

	// simple mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Bridge",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}

	operations := []libovsdb.Operation{insertIntfOp, insertPortOp, mutateOp}
	reply, _ := ovs.Transact("Open_vSwitch", operations...)
	if len(reply) < len(operations) {
		fmt.Println("Number of Replies should be atleast equal to number of Operations")
	}
	ok := true
	for i, o := range reply {
		if o.Error != "" && i < len(operations) {
			fmt.Println("Transaction Failed due to an error :", o.Error, " details:", o.Details, " in ", operations[i])
			ok = false
		} else if o.Error != "" {
			fmt.Println("Transaction Failed due to an error :", o.Error)
			ok = false
		}
	}
	if ok {
		fmt.Println("Port Addition Successful : ", reply[1].UUID.GoUuid)
	}
}

func populateCache(updates libovsdb.TableUpdates) {
	for table, tableUpdate := range updates.Updates {
		if _, ok := cache[table]; !ok {
			cache[table] = make(map[string]libovsdb.Row)

		}
		for uuid, row := range tableUpdate.Rows {
			empty := libovsdb.Row{}
			if !reflect.DeepEqual(row.New, empty) {
				cache[table][uuid] = row.New
			} else {
				delete(cache[table], uuid)
			}
		}
	}
}

func ovs_connect() (*libovsdb.OvsdbClient, error) {
	update = make(chan *libovsdb.TableUpdates)
	cache = make(map[string]map[string]libovsdb.Row)

	// By default libovsdb connects to 127.0.0.0:6400.
	ovs, err := libovsdb.Connect("", 0)
	if err != nil {
		return nil, err
	}
	var notifier Notifier
	ovs.Register(notifier)

	initial, _ := ovs.MonitorAll("Open_vSwitch", "")
	populateCache(*initial)
	return ovs, nil
}

type Notifier struct {
}

func (n Notifier) Update(context interface{}, tableUpdates libovsdb.TableUpdates) {
	populateCache(tableUpdates)
}
func (n Notifier) Locked([]interface{}) {
}
func (n Notifier) Stolen([]interface{}) {
}
func (n Notifier) Echo([]interface{}) {
}
