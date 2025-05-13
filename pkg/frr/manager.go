package frr

import (
	"context"
	"fmt"

	"github.com/telekom/das-schiff-network-operator/pkg/frr/dbus"
)

var (
	frrUnit = "frr.service"
)

//go:generate mockgen -destination ./mock/mock_frr.go . ManagerInterface
type ManagerInterface interface {
	ReloadFRR() error
	RestartFRR() error
	GetStatusFRR() (activeState, subState string, err error)
	SetConfigPath(path string)
}

type Manager struct {
	Cli         *Cli
	dbusToolkit dbus.System
}

func NewFRRManager() *Manager {
	return &Manager{
		Cli:         NewCli(),
		dbusToolkit: &dbus.Toolkit{},
	}
}

func (m *Manager) ReloadFRR() error {
	con, err := m.dbusToolkit.NewConn(context.Background())
	if err != nil {
		return fmt.Errorf("error creating new D-Bus connection: %w", err)
	}
	defer con.Close()

	jobChan := make(chan string)
	if _, err = con.ReloadUnitContext(context.Background(), frrUnit, "fail", jobChan); err != nil {
		return fmt.Errorf("error reloading %s context: %w", frrUnit, err)
	}
	reloadStatus := <-jobChan
	if reloadStatus != "done" {
		return fmt.Errorf("error reloading %s, job status is %s", frrUnit, reloadStatus)
	}
	return nil
}

func (m *Manager) RestartFRR() error {
	con, err := m.dbusToolkit.NewConn(context.Background())
	if err != nil {
		return fmt.Errorf("error creating nee D-Bus connection: %w", err)
	}
	defer con.Close()

	jobChan := make(chan string)
	if _, err = con.RestartUnitContext(context.Background(), frrUnit, "fail", jobChan); err != nil {
		return fmt.Errorf("error restarting %s context: %w", frrUnit, err)
	}
	restartStatus := <-jobChan
	if restartStatus != "done" {
		return fmt.Errorf("error restarting %s, job status is %s", restartStatus, restartStatus)
	}
	return nil
}

func (m *Manager) GetStatusFRR() (activeState, subState string, err error) {
	con, err := m.dbusToolkit.NewConn(context.Background())
	if err != nil {
		return "", "", fmt.Errorf("error creating D-Bus connection: %w", err)
	}
	defer con.Close()

	prop, err := con.GetUnitPropertiesContext(context.Background(), frrUnit)
	if err != nil {
		return "", "", fmt.Errorf("error getting unit %s properties: %w", frrUnit, err)
	}
	var ok bool
	activeState, ok = prop["ActiveState"].(string)
	if !ok {
		return "", "", fmt.Errorf("error casting property %v [\"ActiveState\"] as string", prop)
	}
	subState, ok = prop["SubState"].(string)
	if !ok {
		return activeState, "", fmt.Errorf("error casting property %v [\"SubState\"] as string", prop)
	}
	return activeState, subState, nil
}
