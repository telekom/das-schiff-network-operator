package dbus

import (
	"github.com/godbus/dbus/v5"
)

//go:generate mockgen -destination ./mock/mock_dbus.go . IConn
type IConn interface {
	Auth(methods []dbus.Auth) error
	Close() error
	Hello() error
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
}
