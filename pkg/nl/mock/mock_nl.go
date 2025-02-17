// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/telekom/das-schiff-network-operator/pkg/nl (interfaces: ToolkitInterface)

// Package mock_nl is a generated GoMock package.
package mock_nl

import (
	net "net"
	reflect "reflect"

	netlink "github.com/vishvananda/netlink"
	nl "github.com/vishvananda/netlink/nl"
	gomock "go.uber.org/mock/gomock"
)

// MockToolkitInterface is a mock of ToolkitInterface interface.
type MockToolkitInterface struct {
	ctrl     *gomock.Controller
	recorder *MockToolkitInterfaceMockRecorder
}

// MockToolkitInterfaceMockRecorder is the mock recorder for MockToolkitInterface.
type MockToolkitInterfaceMockRecorder struct {
	mock *MockToolkitInterface
}

// NewMockToolkitInterface creates a new mock instance.
func NewMockToolkitInterface(ctrl *gomock.Controller) *MockToolkitInterface {
	mock := &MockToolkitInterface{ctrl: ctrl}
	mock.recorder = &MockToolkitInterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockToolkitInterface) EXPECT() *MockToolkitInterfaceMockRecorder {
	return m.recorder
}

// AddrAdd mocks base method.
func (m *MockToolkitInterface) AddrAdd(arg0 netlink.Link, arg1 *netlink.Addr) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddrAdd", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddrAdd indicates an expected call of AddrAdd.
func (mr *MockToolkitInterfaceMockRecorder) AddrAdd(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddrAdd", reflect.TypeOf((*MockToolkitInterface)(nil).AddrAdd), arg0, arg1)
}

// AddrDel mocks base method.
func (m *MockToolkitInterface) AddrDel(arg0 netlink.Link, arg1 *netlink.Addr) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddrDel", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddrDel indicates an expected call of AddrDel.
func (mr *MockToolkitInterfaceMockRecorder) AddrDel(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddrDel", reflect.TypeOf((*MockToolkitInterface)(nil).AddrDel), arg0, arg1)
}

// AddrList mocks base method.
func (m *MockToolkitInterface) AddrList(arg0 netlink.Link, arg1 int) ([]netlink.Addr, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddrList", arg0, arg1)
	ret0, _ := ret[0].([]netlink.Addr)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// AddrList indicates an expected call of AddrList.
func (mr *MockToolkitInterfaceMockRecorder) AddrList(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddrList", reflect.TypeOf((*MockToolkitInterface)(nil).AddrList), arg0, arg1)
}

// ExecuteNetlinkRequest mocks base method.
func (m *MockToolkitInterface) ExecuteNetlinkRequest(arg0 *nl.NetlinkRequest, arg1 int, arg2 uint16) ([][]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ExecuteNetlinkRequest", arg0, arg1, arg2)
	ret0, _ := ret[0].([][]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ExecuteNetlinkRequest indicates an expected call of ExecuteNetlinkRequest.
func (mr *MockToolkitInterfaceMockRecorder) ExecuteNetlinkRequest(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ExecuteNetlinkRequest", reflect.TypeOf((*MockToolkitInterface)(nil).ExecuteNetlinkRequest), arg0, arg1, arg2)
}

// LinkAdd mocks base method.
func (m *MockToolkitInterface) LinkAdd(arg0 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkAdd", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkAdd indicates an expected call of LinkAdd.
func (mr *MockToolkitInterfaceMockRecorder) LinkAdd(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkAdd", reflect.TypeOf((*MockToolkitInterface)(nil).LinkAdd), arg0)
}

// LinkByIndex mocks base method.
func (m *MockToolkitInterface) LinkByIndex(arg0 int) (netlink.Link, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkByIndex", arg0)
	ret0, _ := ret[0].(netlink.Link)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkByIndex indicates an expected call of LinkByIndex.
func (mr *MockToolkitInterfaceMockRecorder) LinkByIndex(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkByIndex", reflect.TypeOf((*MockToolkitInterface)(nil).LinkByIndex), arg0)
}

// LinkByName mocks base method.
func (m *MockToolkitInterface) LinkByName(arg0 string) (netlink.Link, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkByName", arg0)
	ret0, _ := ret[0].(netlink.Link)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkByName indicates an expected call of LinkByName.
func (mr *MockToolkitInterfaceMockRecorder) LinkByName(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkByName", reflect.TypeOf((*MockToolkitInterface)(nil).LinkByName), arg0)
}

// LinkDel mocks base method.
func (m *MockToolkitInterface) LinkDel(arg0 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkDel", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkDel indicates an expected call of LinkDel.
func (mr *MockToolkitInterfaceMockRecorder) LinkDel(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkDel", reflect.TypeOf((*MockToolkitInterface)(nil).LinkDel), arg0)
}

// LinkGetProtinfo mocks base method.
func (m *MockToolkitInterface) LinkGetProtinfo(arg0 netlink.Link) (netlink.Protinfo, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkGetProtinfo", arg0)
	ret0, _ := ret[0].(netlink.Protinfo)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkGetProtinfo indicates an expected call of LinkGetProtinfo.
func (mr *MockToolkitInterfaceMockRecorder) LinkGetProtinfo(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkGetProtinfo", reflect.TypeOf((*MockToolkitInterface)(nil).LinkGetProtinfo), arg0)
}

// LinkList mocks base method.
func (m *MockToolkitInterface) LinkList() ([]netlink.Link, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkList")
	ret0, _ := ret[0].([]netlink.Link)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkList indicates an expected call of LinkList.
func (mr *MockToolkitInterfaceMockRecorder) LinkList() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkList", reflect.TypeOf((*MockToolkitInterface)(nil).LinkList))
}

// LinkSetDown mocks base method.
func (m *MockToolkitInterface) LinkSetDown(arg0 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetDown", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetDown indicates an expected call of LinkSetDown.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetDown(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetDown", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetDown), arg0)
}

// LinkSetHairpin mocks base method.
func (m *MockToolkitInterface) LinkSetHairpin(arg0 netlink.Link, arg1 bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetHairpin", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetHairpin indicates an expected call of LinkSetHairpin.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetHairpin(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetHairpin", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetHairpin), arg0, arg1)
}

// LinkSetHardwareAddr mocks base method.
func (m *MockToolkitInterface) LinkSetHardwareAddr(arg0 netlink.Link, arg1 net.HardwareAddr) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetHardwareAddr", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetHardwareAddr indicates an expected call of LinkSetHardwareAddr.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetHardwareAddr(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetHardwareAddr", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetHardwareAddr), arg0, arg1)
}

// LinkSetLearning mocks base method.
func (m *MockToolkitInterface) LinkSetLearning(arg0 netlink.Link, arg1 bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetLearning", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetLearning indicates an expected call of LinkSetLearning.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetLearning(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetLearning", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetLearning), arg0, arg1)
}

// LinkSetMTU mocks base method.
func (m *MockToolkitInterface) LinkSetMTU(arg0 netlink.Link, arg1 int) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetMTU", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetMTU indicates an expected call of LinkSetMTU.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetMTU(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetMTU", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetMTU), arg0, arg1)
}

// LinkSetMaster mocks base method.
func (m *MockToolkitInterface) LinkSetMaster(arg0, arg1 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetMaster", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetMaster indicates an expected call of LinkSetMaster.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetMaster(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetMaster", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetMaster), arg0, arg1)
}

// LinkSetMasterByIndex mocks base method.
func (m *MockToolkitInterface) LinkSetMasterByIndex(arg0 netlink.Link, arg1 int) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetMasterByIndex", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetMasterByIndex indicates an expected call of LinkSetMasterByIndex.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetMasterByIndex(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetMasterByIndex", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetMasterByIndex), arg0, arg1)
}

// LinkSetNoMaster mocks base method.
func (m *MockToolkitInterface) LinkSetNoMaster(arg0 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetNoMaster", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetNoMaster indicates an expected call of LinkSetNoMaster.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetNoMaster(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetNoMaster", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetNoMaster), arg0)
}

// LinkSetUp mocks base method.
func (m *MockToolkitInterface) LinkSetUp(arg0 netlink.Link) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkSetUp", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkSetUp indicates an expected call of LinkSetUp.
func (mr *MockToolkitInterfaceMockRecorder) LinkSetUp(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkSetUp", reflect.TypeOf((*MockToolkitInterface)(nil).LinkSetUp), arg0)
}

// NeighList mocks base method.
func (m *MockToolkitInterface) NeighList(arg0, arg1 int) ([]netlink.Neigh, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NeighList", arg0, arg1)
	ret0, _ := ret[0].([]netlink.Neigh)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// NeighList indicates an expected call of NeighList.
func (mr *MockToolkitInterfaceMockRecorder) NeighList(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NeighList", reflect.TypeOf((*MockToolkitInterface)(nil).NeighList), arg0, arg1)
}

// NewIPNet mocks base method.
func (m *MockToolkitInterface) NewIPNet(arg0 net.IP) *net.IPNet {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewIPNet", arg0)
	ret0, _ := ret[0].(*net.IPNet)
	return ret0
}

// NewIPNet indicates an expected call of NewIPNet.
func (mr *MockToolkitInterfaceMockRecorder) NewIPNet(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewIPNet", reflect.TypeOf((*MockToolkitInterface)(nil).NewIPNet), arg0)
}

// ParseAddr mocks base method.
func (m *MockToolkitInterface) ParseAddr(arg0 string) (*netlink.Addr, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ParseAddr", arg0)
	ret0, _ := ret[0].(*netlink.Addr)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ParseAddr indicates an expected call of ParseAddr.
func (mr *MockToolkitInterfaceMockRecorder) ParseAddr(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ParseAddr", reflect.TypeOf((*MockToolkitInterface)(nil).ParseAddr), arg0)
}

// RouteAdd mocks base method.
func (m *MockToolkitInterface) RouteAdd(arg0 *netlink.Route) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RouteAdd", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RouteAdd indicates an expected call of RouteAdd.
func (mr *MockToolkitInterfaceMockRecorder) RouteAdd(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RouteAdd", reflect.TypeOf((*MockToolkitInterface)(nil).RouteAdd), arg0)
}

// RouteDel mocks base method.
func (m *MockToolkitInterface) RouteDel(arg0 *netlink.Route) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RouteDel", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RouteDel indicates an expected call of RouteDel.
func (mr *MockToolkitInterfaceMockRecorder) RouteDel(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RouteDel", reflect.TypeOf((*MockToolkitInterface)(nil).RouteDel), arg0)
}

// RouteListFiltered mocks base method.
func (m *MockToolkitInterface) RouteListFiltered(arg0 int, arg1 *netlink.Route, arg2 uint64) ([]netlink.Route, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RouteListFiltered", arg0, arg1, arg2)
	ret0, _ := ret[0].([]netlink.Route)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// RouteListFiltered indicates an expected call of RouteListFiltered.
func (mr *MockToolkitInterfaceMockRecorder) RouteListFiltered(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RouteListFiltered", reflect.TypeOf((*MockToolkitInterface)(nil).RouteListFiltered), arg0, arg1, arg2)
}

// VethPeerIndex mocks base method.
func (m *MockToolkitInterface) VethPeerIndex(arg0 *netlink.Veth) (int, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "VethPeerIndex", arg0)
	ret0, _ := ret[0].(int)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// VethPeerIndex indicates an expected call of VethPeerIndex.
func (mr *MockToolkitInterfaceMockRecorder) VethPeerIndex(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "VethPeerIndex", reflect.TypeOf((*MockToolkitInterface)(nil).VethPeerIndex), arg0)
}
