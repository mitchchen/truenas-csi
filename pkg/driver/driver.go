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

func (d *Driver) Run() error {
	// Parse the endpoint
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

	// Create listener
	listener, err := net.Listen(scheme, addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(logGRPC),
	}
	d.srv = grpc.NewServer(opts...)

	klog.Infof("Starting CSI driver %s version %s on endpoint %s", d.name, d.version, d.endpoint)

	go d.handleShutdown()

	return d.srv.Serve(listener)
}

func (d *Driver) Stop() {
	klog.Info("Stopping CSI driver server")
	d.srv.GracefulStop()
}

func (d *Driver) handleShutdown() {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	d.Stop()
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
		klog.Errorf("GRPC error: %s: %v", info.FullMethod, err)
	}
	return resp, err
}
