package dbus

import (
	"context"
	"fmt"

	"github.com/coreos/go-systemd/v22/dbus"
)

//go:generate mockgen -destination ./mock/mock_dbus.go . System,Connection
type System interface {
	NewConn(ctx context.Context) (Connection, error)
}

type Connection interface {
	Close()
	ReloadUnitContext(context.Context, string, string, chan<- string) (int, error)
	GetUnitPropertiesContext(ctx context.Context, unit string) (map[string]interface{}, error)
	RestartUnitContext(ctx context.Context, name string, mode string, ch chan<- string) (int, error)
}

type Toolkit struct{}

func (*Toolkit) NewConn(ctx context.Context) (Connection, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("error creating new D-Bus connection: %w", err)
	}
	return conn, nil
}
