package driver

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

const (
	driverName    = "csi.truenas.io"
	driverVersion = "0.1.0"
)

type Driver struct {
	name     string
	version  string
	endpoint string
	nodeID   string

	srv *grpc.Server
}

func NewDriver(endpoint, nodeID string) *Driver {
	return &Driver{
		name:     driverName,
		version:  driverVersion,
		endpoint: endpoint,
		nodeID:   nodeID,
	}
}

func (d *Driver) Run(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			klog.V(2).ErrorS(nil, "Recovered from panic in CSI driver")
		}
	}()

	scheme, addr, err := parseEndpoint(d.endpoint)
	if err != nil {
		return err
	}

	// Remove existing socket file if it exists
	if scheme == "unix" {
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove existing socket: %v", err)
		}
	}

	listener, err := net.Listen(scheme, addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(logGRPC),
	}
	d.srv = grpc.NewServer(opts...)

	klog.V(2).Infof("Starting CSI driver %s version %s on endpoint %s", d.name, d.version, d.endpoint)

	serverErr := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.V(2).ErrorS(nil, "Recovered from panic in gRPC server")
				serverErr <- fmt.Errorf("server panic: %v", r)
			}
		}()
		if err := d.srv.Serve(listener); err != nil {
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
	d.srv.GracefulStop()
}

func parseEndpoint(ep string) (string, string, error) {
	if ep == "" {
		return "", "", fmt.Errorf("endpoint is empty")
	}

	// Default to unix socket
	if !strings.Contains(ep, "://") {
		return "unix", ep, nil
	}

	u, err := url.Parse(ep)
	if err != nil {
		return "", "", err
	}

	switch u.Scheme {
	case "unix":
		return "unix", u.Path, nil
	case "tcp":
		return "tcp", u.Host, nil
	default:
		return "", "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func logGRPC(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	klog.V(2).Infof("GRPC call: %s", info.FullMethod)
	resp, err := handler(ctx, req)
	if err != nil {
		klog.V(2).ErrorS(err, "GRPC error:"+info.FullMethod)
	}
	return resp, err
}
