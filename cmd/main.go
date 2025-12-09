package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iXsystems/truenas_k8_driver/pkg/driver"
	"k8s.io/klog/v2"
)

var (
	// CSI protocol flags only
	endpoint      = flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	nodeID        = flag.String("node-id", "", "Node ID")
	driverName    = flag.String("driver-name", driver.DRIVER_NAME, "Name of the driver")
	driverVersion = flag.String("driver-version", driver.DRIVER_VERSION, "Version of the driver")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Starting TrueNAS CSI Driver version %s", *driverVersion)

	if err := validateFlags(); err != nil {
		klog.Fatalf("Invalid configuration: %v", err)
	}

	config := &driver.DriverConfig{
		DriverName:    *driverName,
		DriverVersion: *driverVersion,
		NodeID:        *nodeID,
		Endpoint:      *endpoint,
	}

	if err := loadEnvConfig(config); err != nil {
		klog.Fatalf("Invalid configuration: %v", err)
	}

	d, err := driver.NewDriver(config)
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(context.Background())
	}()

	select {
	case err := <-errCh:
		if err != nil {
			klog.Fatalf("Driver failed: %v", err)
		}
	case sig := <-sigCh:
		klog.Infof("Received signal %v, shutting down", sig)
		d.Stop()
	}

	klog.Info("TrueNAS CSI Driver stopped")
}

func validateFlags() error {
	if *nodeID == "" {
		if hostname, err := os.Hostname(); err == nil {
			*nodeID = hostname
		} else {
			return fmt.Errorf("Node ID is required but could not be determined")
		}
	}

	if *endpoint == "" {
		return fmt.Errorf("--endpoint is required")
	}

	return nil
}

func loadEnvConfig(config *driver.DriverConfig) error {
	if val := os.Getenv("TRUENAS_URL"); val == "" {
		return fmt.Errorf("TRUENAS_URL is missing")
	} else {
		config.TrueNASURL = val
	}

	if val := os.Getenv("TRUENAS_API_KEY"); val == "" {
		return fmt.Errorf("TRUENAS_API_KEY is missing")
	} else {
		config.TrueNASAPIKey = val
	}

	if val := os.Getenv("TRUENAS_DEFAULT_POOL"); val == "" {
		return fmt.Errorf("TRUENAS_DEFAULT_POOL is missing")
	} else {
		config.DefaultPool = val
	}

	if val := os.Getenv("TRUENAS_NFS_SERVER"); val == "" {
		return fmt.Errorf("TRUENAS_NFS_SERVER is missing")
	} else {
		config.NFSServer = val
	}

	if val := os.Getenv("TRUENAS_ISCSI_PORTAL"); val == "" {
		return fmt.Errorf("TRUENAS_ISCSI_PORTAL is missing")
	} else {
		config.ISCSIPortal = val
	}

	if val := os.Getenv("TRUENAS_ISCSI_IQN_BASE"); val != "" {
		config.ISCSIIQNBase = val
	}

	if val := os.Getenv("TRUENAS_INSECURE_SKIP_VERIFY"); val == "true" {
		config.TrueNASInsecure = true
	}

	return nil
}
