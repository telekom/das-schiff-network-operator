package frr

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type FRRCLI struct {
	binaryPath string
}

type ConifgurationTransaction struct {
	err     error
	success func(interface{})
	reply   interface{}
}

func NewConfigurationTransaction() *ConifgurationTransaction {
	return &ConifgurationTransaction{}
}

type VRFDO struct {
}

func NewFRRCLI() (*FRRCLI, error) {
	return &FRRCLI{
		binaryPath: "/usr/bin/vtysh",
	}, nil
}

func (frr *FRRCLI) execute(args []string) []byte {
	// Ensure JSON is always appended
	args = append(args, "json")
	joinedArgs := strings.Join(args[:], " ")
	cmd := &exec.Cmd{
		Path: frr.binaryPath,
		Args: append([]string{"-c"}, joinedArgs),
	}
	output, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("Could not run the command, %s %s, output: %s", frr.binaryPath, strings.Join(cmd.Args, " "), cmd.Stderr))
	}
	return output
}
func (transaction *ConifgurationTransaction) Do(cb func(frr FRRCLI)) *ConifgurationTransaction {
	//pool is a global object that has been setup in my app
	// c.Send("MULTI")
	// cb(c)
	// reply, err := c.Do("EXEC")
	// t.reply = reply
	// t.err = err
	return transaction
}

func (transaction *ConifgurationTransaction) OnFail(cb func(err error)) *ConifgurationTransaction {
	if transaction.err != nil {
		cb(transaction.err)
	} else {
		transaction.success(transaction.reply)
	}
	return transaction
}

func (transaction *ConifgurationTransaction) OnSuccess(cb func(reply interface{})) *ConifgurationTransaction {
	transaction.success = cb
	return transaction
}

func (frr *FRRCLI) ShowVRFs() {
	data := frr.execute([]string{
		"show",
		"vrf",
		"vni",
		"json",
	})
	vrfInfo := VRFDO{}
	json.Unmarshal(data, vrfInfo)
}

func (frr *FRRCLI) ShowIPRoute() {
	frr.execute([]string{
		"show",
		"ip",
		"route",
	})
	// 	{
	// 		"prefix":"192.168.255.93/32",
	// 		"prefixLen":32,
	// 		"protocol":"bgp",
	// 		"vrfId":0,
	// 		"vrfName":"default",
	// 		"selected":true,
	// 		"destSelected":true,
	// 		"distance":20,
	// 		"metric":0,
	// 		"installed":true,
	// 		"tag":20000,
	// 		"table":254,
	// 		"internalStatus":16,
	// 		"internalFlags":8,
	// 		"internalNextHopNum":1,
	// 		"internalNextHopActiveNum":1,
	// 		"nexthopGroupId":832,
	// 		"installedNexthopGroupId":832,
	// 		"uptime":"01w6d00h",
	// 		"nexthops":[
	// 		  {
	// 			"flags":267,
	// 			"fib":true,
	// 			"ip":"192.168.2.153",
	// 			"afi":"ipv4",
	// 			"interfaceIndex":20,
	// 			"interfaceName":"br.cluster",
	// 			"active":true,
	// 			"onLink":true,
	// 			"weight":1
	// 		  }
	// 		]
	// 	  }
	// 	]
	//   }
}
