package driver

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/iXsystems/truenas_k8_driver/pkg/client"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

const (
	DRIVER_NAME = "csi.truenas.io"

	DRIVER_VERSION = "1.0.0"

	TopologyKey = "topology.truenas.csi/zone"

	DEFAULT_IQN_BASE = "iqn.2000-01.io.truenas"
)

type Driver struct {
	name     string
	version  string
	nodeID   string
	endpoint string

	client *client.APIClient

	defaultPool  string
	nfsServer    string
	iscsiPortal  string
	iscsiIQNBase string

	identityServer   csi.IdentityServer
	controllerServer csi.ControllerServer
	nodeServer       csi.NodeServer

	server *grpc.Server

	volumes      map[string]*VolumeInfo
	volumesMutex sync.RWMutex

	controllerCaps []*csi.ControllerServiceCapability
	nodeCaps       []*csi.NodeServiceCapability
	pluginCaps     []*csi.PluginCapability
	volumeCaps     []*csi.VolumeCapability_AccessMode
}

type VolumeInfo struct {
	ID                 string
	Name               string
	CapacityBytes      int64
	VolumeContext      map[string]string
	ContentSource      *csi.VolumeContentSource
	AccessibleTopology []*csi.Topology

	DatasetPath string
	PoolName    string
	Protocol    string // "nfs" or "iscsi"

	NFSPath    string
	NFSShareID int

	TargetIQN     string
	TargetPortal  string
	LUN           int
	ISCSITargetID int
	ISCSIExtentID int
}

type DriverConfig struct {
	DriverName    string
	DriverVersion string
	NodeID        string
	Endpoint      string

	TrueNASURL      string
	TrueNASAPIKey   string
	TrueNASInsecure bool

	DefaultPool  string
	NFSServer    string
	ISCSIPortal  string
	ISCSIIQNBase string
}

func NewDriver(config *DriverConfig) (*Driver, error) {
	if config.DriverName == "" {
		config.DriverName = DRIVER_NAME
	}
	if config.DriverVersion == "" {
		config.DriverVersion = DRIVER_VERSION
	}

	if config.NodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}
	if config.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if config.TrueNASURL == "" {
		return nil, fmt.Errorf("TrueNAS URL is required")
	}
	if config.TrueNASAPIKey == "" {
		return nil, fmt.Errorf("TrueNAS API key is required")
	}
	if config.DefaultPool == "" {
		return nil, fmt.Errorf("default pool is required")
	}

	if config.ISCSIIQNBase == "" {
		config.ISCSIIQNBase = DEFAULT_IQN_BASE
	}

	if err := validateIQNFormat(config.ISCSIIQNBase); err != nil {
		return nil, fmt.Errorf("invalid iSCSI IQN base format: %w", err)
	}

	tnURL, err := url.Parse(config.TrueNASURL)
	if err != nil {
		return nil, fmt.Errorf("Invalid TrueNAS URL")
	}

	clientConfig := &client.ClientConfig{
		URL:                *tnURL,
		APIKey:             config.TrueNASAPIKey,
		InsecureSkipVerify: config.TrueNASInsecure,
	}

	truenasClient, err := client.NewClient(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create TrueNAS client: %w", err)
	}

	// Test connection
	ctx := context.Background()
	if err := truenasClient.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}

	// Validate that the default pool exists
	klog.V(2).Infof("Validating pool '%s' exists in TrueNAS", config.DefaultPool)
	pool, err := truenasClient.GetPool(ctx, config.DefaultPool)
	if err != nil {
		return nil, fmt.Errorf("failed to validate pool '%s': %w\n\nPlease create the pool in TrueNAS UI (Storage → Create Pool) before using the CSI driver", config.DefaultPool, err)
	}
	klog.V(2).Infof("Pool '%s' validated successfully (GUID: %s)", config.DefaultPool, pool.GUID)

	d := &Driver{
		name:         config.DriverName,
		version:      config.DriverVersion,
		nodeID:       config.NodeID,
		endpoint:     config.Endpoint,
		client:       truenasClient,
		defaultPool:  config.DefaultPool,
		nfsServer:    config.NFSServer,
		iscsiPortal:  config.ISCSIPortal,
		iscsiIQNBase: config.ISCSIIQNBase,
		volumes:      make(map[string]*VolumeInfo),
	}

	d.initializeCapabilities()

	d.identityServer = NewIdentityServer(d)
	d.controllerServer = NewControllerServer(d)
	d.nodeServer = NewNodeServer(d)

	return d, nil
}

func (d *Driver) Run(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			klog.V(2).ErrorS(nil, "Recovered from panic in CSI driver")
		}
	}()

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	u, err := url.Parse(d.endpoint)
	if err != nil {
		return fmt.Errorf("failed to parse endpoint: %w", err)
	}

	var addr string
	switch u.Scheme {
	case "unix":
		addr = u.Path
		// Remove existing socket file if it exists
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove existing socket: %w", err)
		}

		// Create directory if needed
		if err := os.MkdirAll(filepath.Dir(addr), 0o755); err != nil {
			return fmt.Errorf("failed to create socket directory: %w", err)
		}
	case "tcp":
		addr = u.Host
	default:
		return fmt.Errorf("unsupported endpoint scheme: %s", u.Scheme)
	}

	listener, err := net.Listen(u.Scheme, addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(d.unaryInterceptor),
	}
	d.server = grpc.NewServer(opts...)

	csi.RegisterIdentityServer(d.server, d.identityServer)
	csi.RegisterControllerServer(d.server, d.controllerServer)
	csi.RegisterNodeServer(d.server, d.nodeServer)

	serverErr := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.V(2).ErrorS(nil, "Recovered from panic in gRPC server")
				serverErr <- fmt.Errorf("server panic: %v", r)
			}
		}()

		klog.Infof("TrueNAS CSI driver %s version %s starting on %s", d.name, d.version, d.endpoint)
		if err := d.server.Serve(listener); err != nil {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		klog.V(2).Info("Shutdown signal recieved, stopping CSI driver")
		d.Stop()
		return nil
	case err := <-serverErr:
		klog.V(2).ErrorS(err, "Server error occurred")
		d.Stop()
		return fmt.Errorf("server error: %v", err)
	}
}

func (d *Driver) Stop() {
	klog.V(2).Info("Stopping CSI driver server")
	d.server.GracefulStop()
	d.client.Close()
	klog.Info("TrueNAS CSI driver stopped")
}

func (d *Driver) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	klog.V(4).Infof("GRPC call: %s", info.FullMethod)
	klog.V(5).Infof("GRPC request: %+v", req)

	resp, err := handler(ctx, req)

	if err != nil {
		klog.Errorf("GRPC call %s failed: %v", info.FullMethod, err)
	} else {
		klog.V(5).Infof("GRPC response: %+v", resp)
	}

	return resp, err
}

func (d *Driver) initializeCapabilities() {
	d.controllerCaps = []*csi.ControllerServiceCapability{
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_GET_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_VOLUME_CONDITION,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
				},
			},
		},
	}

	// Node capabilities
	d.nodeCaps = []*csi.NodeServiceCapability{
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
				},
			},
		},
	}

	// Plugin capabilities
	d.pluginCaps = []*csi.PluginCapability{
		{
			Type: &csi.PluginCapability_Service_{
				Service: &csi.PluginCapability_Service{
					Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
				},
			},
		},
		{
			Type: &csi.PluginCapability_Service_{
				Service: &csi.PluginCapability_Service{
					Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
				},
			},
		},
		{
			Type: &csi.PluginCapability_VolumeExpansion_{
				VolumeExpansion: &csi.PluginCapability_VolumeExpansion{
					Type: csi.PluginCapability_VolumeExpansion_ONLINE,
				},
			},
		},
	}

	// Volume access modes
	d.volumeCaps = []*csi.VolumeCapability_AccessMode{
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY},
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER},
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER},
		{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY},
		{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER},
		{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
	}
}

func (d *Driver) generateVolumeID(pool, name string) string {
	return fmt.Sprintf("%s/%s", pool, name)
}

func (d *Driver) parseVolumeID(volumeID string) (pool, name string, err error) {
	parts := strings.SplitN(volumeID, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid volume ID format: %s", volumeID)
	}
	return parts[0], parts[1], nil
}

func (d *Driver) getProtocolFromParameters(parameters map[string]string) string {
	if protocol, ok := parameters["protocol"]; ok {
		return strings.ToLower(protocol)
	}
	// Default to NFS if not specified
	return "nfs"
}

func (d *Driver) getPoolFromParameters(parameters map[string]string) string {
	if pool, ok := parameters["pool"]; ok {
		return pool
	}
	return d.defaultPool
}

func (d *Driver) getISCSIIQNBaseFromParameters(parameters map[string]string) string {
	// Check for iscsi.iqn-base or iscsi.iqn-prefix in parameters
	if iqnBase, ok := parameters["iscsi.iqn-base"]; ok {
		return iqnBase
	}
	if iqnBase, ok := parameters["iscsi.iqn-prefix"]; ok {
		return iqnBase
	}
	// Fall back to driver default
	return d.iscsiIQNBase
}

func validateIQNFormat(iqn string) error {
	// IQN format: iqn.YYYY-MM.reverse.domain.name[:identifier]
	// Example: iqn.2024-01.com.example or iqn.2024-01.com.example:storage
	if !strings.HasPrefix(iqn, "iqn.") {
		return fmt.Errorf("IQN must start with 'iqn.'")
	}

	// Must have at least: iqn.YYYY-MM.domain
	parts := strings.Split(iqn, ".")
	if len(parts) < 3 {
		return fmt.Errorf("IQN format invalid: must be iqn.YYYY-MM.domain (got: %s)", iqn)
	}

	// Validate date format (YYYY-MM)
	dateField := parts[1]
	if len(dateField) != 7 || dateField[4] != '-' {
		return fmt.Errorf("IQN date field must be YYYY-MM format (got: %s)", dateField)
	}

	return nil
}

func sanitizeVolumeName(name string) string {
	// Replace invalid characters with underscores
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "@", "_")
	name = strings.ReplaceAll(name, "#", "_")

	// Ensure it doesn't start with a number or special character
	if len(name) > 0 && (name[0] < 'A' || (name[0] > 'Z' && name[0] < 'a') || name[0] > 'z') {
		name = "vol_" + name
	}

	return name
}

func (d *Driver) getVolumeInfo(volumeID string) (*VolumeInfo, error) {
	// Fast path: check memory cache
	d.volumesMutex.RLock()
	vol, ok := d.volumes[volumeID]
	d.volumesMutex.RUnlock()

	if ok {
		return vol, nil
	}

	// Slow path: reconstruct from TrueNAS
	klog.V(4).Infof("Volume %s not in cache, reconstructing from TrueNAS", volumeID)
	return d.reconstructVolumeFromTrueNAS(volumeID)
}

func (d *Driver) reconstructVolumeFromTrueNAS(volumeID string) (*VolumeInfo, error) {
	ctx := context.Background()

	// Parse volume ID (format: pool/dataset-name)
	pool, datasetName, err := d.parseVolumeID(volumeID)
	if err != nil {
		return nil, fmt.Errorf("invalid volume ID %s: %w", volumeID, err)
	}

	datasetPath := pool + "/" + datasetName

	// Query TrueNAS for the dataset
	dataset, err := d.client.GetDataset(ctx, datasetPath)
	if err != nil {
		return nil, fmt.Errorf("volume %s not found in TrueNAS: %w", volumeID, err)
	}

	// Reconstruct volume info based on dataset type
	volInfo := &VolumeInfo{
		ID:            volumeID,
		Name:          volumeID,
		DatasetPath:   datasetPath,
		PoolName:      pool,
		VolumeContext: make(map[string]string),
	}

	// Determine protocol and capacity based on dataset type
	if dataset.Type == "VOLUME" {
		volInfo.Protocol = ISCSI
		volInfo.CapacityBytes = dataset.Used

		// Fallback to RefQuota if Used is 0
		if volInfo.CapacityBytes == 0 {
			volInfo.CapacityBytes = dataset.RefQuota
		}

		// iSCSI target details would need to be queried separately if needed
		klog.V(4).Infof("Reconstructed iSCSI volume: %s (capacity: %d bytes)", volumeID, volInfo.CapacityBytes)
	} else {
		// NFS filesystem
		volInfo.Protocol = NFS
		volInfo.CapacityBytes = dataset.RefQuota
		volInfo.NFSPath = dataset.Mountpoint

		if d.nfsServer != "" {
			volInfo.VolumeContext["nfsServer"] = d.nfsServer
		}
		volInfo.VolumeContext["nfsPath"] = dataset.Mountpoint

		klog.V(4).Infof("Reconstructed NFS volume: %s (capacity: %d bytes, path: %s)", volumeID, volInfo.CapacityBytes, dataset.Mountpoint)
	}

	// Store reconstructed volume info in cache
	d.storeVolumeInfo(volInfo)

	klog.V(2).Infof("Successfully reconstructed volume %s from TrueNAS", volumeID)
	return volInfo, nil
}

func (d *Driver) storeVolumeInfo(vol *VolumeInfo) {
	d.volumesMutex.Lock()
	defer d.volumesMutex.Unlock()

	d.volumes[vol.ID] = vol
}

func (d *Driver) deleteVolumeInfo(volumeID string) {
	d.volumesMutex.Lock()
	defer d.volumesMutex.Unlock()

	delete(d.volumes, volumeID)
}
