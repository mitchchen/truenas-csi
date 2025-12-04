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
	endpoint      = flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	nodeID        = flag.String("node-id", "", "Node ID")
	driverName    = flag.String("driver-name", driver.DRIVER_NAME, "Name of the driver")
	driverVersion = flag.String("driver-version", driver.DRIVER_VERSION, "Version of the driver")

	truenasURL      = flag.String("truenas-url", "", "TrueNAS WebSocket API URL (e.g., wss://truenas.example.com/api/v2.0)")
	truenasAPIKey   = flag.String("truenas-api-key", "", "TrueNAS API key (optional if using username/password)")
	truenasUsername = flag.String("truenas-username", "", "TrueNAS username (optional if using API key)")
	truenasPassword = flag.String("truenas-password", "", "TrueNAS password (optional if using API key)")
	truenasInsecure = flag.Bool("truenas-insecure", false, "Skip TLS verification for TrueNAS connection")

	defaultPool  = flag.String("default-pool", "", "Default storage pool")
	nfsServer    = flag.String("nfs-server", "", "NFS server address (usually the TrueNAS IP)")
	iscsiPortal  = flag.String("iscsi-portal", "", "iSCSI portal address (e.g., 192.168.1.100:3260)")
	iscsiIQNBase = flag.String("iscsi-iqn-base", "", "iSCSI IQN base prefix (e.g., iqn.2024-01.com.example) - defaults to iqn.2000-01.io.truenas")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Infof("Starting TrueNAS CSI Driver version %s", *driverVersion)

	if err := validateFlags(); err != nil {
		klog.Fatalf("Invalid configuration: %v", err)
	}

	if *nodeID == "" {
		if hostname, err := os.Hostname(); err == nil {
			*nodeID = hostname
		} else {
			klog.Fatal("Node ID is required but could not be determined")
		}
	}

	config := &driver.DriverConfig{
		DriverName:      *driverName,
		DriverVersion:   *driverVersion,
		NodeID:          *nodeID,
		Endpoint:        *endpoint,
		TrueNASURL:      *truenasURL,
		TrueNASAPIKey:   *truenasAPIKey,
		TrueNASUsername: *truenasUsername,
		TrueNASPassword: *truenasPassword,
		TrueNASInsecure: *truenasInsecure,
		DefaultPool:     *defaultPool,
		NFSServer:       *nfsServer,
		ISCSIPortal:     *iscsiPortal,
		ISCSIIQNBase:    *iscsiIQNBase,
	}

	loadEnvConfig(config)

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
	if *truenasURL == "" {
		return fmt.Errorf("--truenas-url is required")
	}

	// Require either API key OR username+password
	if *truenasAPIKey == "" && (*truenasUsername == "" || *truenasPassword == "") {
		return fmt.Errorf("TrueNAS authentication required: provide either --truenas-api-key OR --truenas-username and --truenas-password")
	}

	if *defaultPool == "" {
		return fmt.Errorf("--default-pool is required")
	}

	if *endpoint == "" {
		return fmt.Errorf("--endpoint is required")
	}

	return nil
}

func loadEnvConfig(config *driver.DriverConfig) {
	if config.TrueNASURL == "" {
		if val := os.Getenv("TRUENAS_URL"); val != "" {
			config.TrueNASURL = val
		}
	}

	if config.TrueNASAPIKey == "" {
		if val := os.Getenv("TRUENAS_API_KEY"); val != "" {
			config.TrueNASAPIKey = val
		}
	}

	if config.TrueNASUsername == "" {
		if val := os.Getenv("TRUENAS_USERNAME"); val != "" {
			config.TrueNASUsername = val
		}
	}

	if config.TrueNASPassword == "" {
		if val := os.Getenv("TRUENAS_PASSWORD"); val != "" {
			config.TrueNASPassword = val
		}
	}

	if config.DefaultPool == "" {
		if val := os.Getenv("TRUENAS_DEFAULT_POOL"); val != "" {
			config.DefaultPool = val
		}
	}

	if config.NFSServer == "" {
		if val := os.Getenv("TRUENAS_NFS_SERVER"); val != "" {
			config.NFSServer = val
		}
	}

	if config.ISCSIPortal == "" {
		if val := os.Getenv("TRUENAS_ISCSI_PORTAL"); val != "" {
			config.ISCSIPortal = val
		}
	}

	if config.ISCSIIQNBase == "" {
		if val := os.Getenv("TRUENAS_ISCSI_IQN_BASE"); val != "" {
			config.ISCSIIQNBase = val
		}
	}

	if config.NodeID == "" {
		if val := os.Getenv("NODE_ID"); val != "" {
			config.NodeID = val
		}
	}

	if val := os.Getenv("TRUENAS_INSECURE_SKIP_VERIFY"); val == "true" {
		config.TrueNASInsecure = true
	}
}
