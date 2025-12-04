package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

type IdentityServer struct {
	driver *Driver
	csi.UnimplementedIdentityServer
}

func NewIdentityServer(driver *Driver) *IdentityServer {
	return &IdentityServer{
		driver: driver,
	}
}

func (s *IdentityServer) GetPluginInfo(ctx context.Context, req *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	klog.V(4).Infof("GetPluginInfo called")

	if s.driver.name == "" {
		return nil, status.Error(codes.Unavailable, "driver name not configured")
	}

	if s.driver.version == "" {
		return nil, status.Error(codes.Unavailable, "driver version not configured")
	}

	return &csi.GetPluginInfoResponse{
		Name:          s.driver.name,
		VendorVersion: s.driver.version,
	}, nil
}

func (s *IdentityServer) GetPluginCapabilities(ctx context.Context, req *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	klog.V(4).Infof("GetPluginCapabilities called")

	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: s.driver.pluginCaps,
	}, nil
}

func (s *IdentityServer) Probe(ctx context.Context, req *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	klog.V(4).Infof("Probe called")

	if err := s.driver.client.Ping(ctx); err != nil {
		klog.Errorf("Health check failed: %v", err)
		return nil, status.Error(codes.FailedPrecondition, "TrueNAS connection failed")
	}

	return &csi.ProbeResponse{}, nil
}
