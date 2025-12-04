package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
	"k8s.io/utils/exec"
)

type NodeServer struct {
	driver  *Driver
	mounter mount.Interface
	csi.UnimplementedNodeServer
}

func NewNodeServer(driver *Driver) *NodeServer {
	return &NodeServer{
		driver:  driver,
		mounter: mount.New(""),
	}
}

func (s *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.V(4).Infof("NodeStageVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err != nil {
		// If volume info not found, try to reconstruct from publish context
		volInfo = s.reconstructVolumeInfo(req.VolumeId, req.PublishContext, req.VolumeContext)
	}

	if volInfo.Protocol != "iscsi" {
		klog.V(4).Infof("Volume %s is NFS, skipping staging", req.VolumeId)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// For iSCSI:
	// 1. Discover and login to the target
	// 2. Find the device
	// 3. Format if necessary
	// 4. Mount to staging path

	targetPortal := req.PublishContext["targetPortal"]
	targetIQN := req.PublishContext["targetIQN"]
	lun := req.PublishContext["lun"]

	if targetPortal == "" || targetIQN == "" {
		return nil, status.Error(codes.InvalidArgument, "iSCSI target information missing")
	}

	executor := exec.New()
	// Run discovery
	output, err := executor.Command("/usr/sbin/iscsiadm", "-m", "discovery", "-t", "sendtargets", "-p", targetPortal).CombinedOutput()
	if err != nil {
		klog.Errorf("iSCSI discovery failed: %v, output: %s", err, string(output))
	}

	// Login to target
	output, err = executor.Command("/usr/sbin/iscsiadm", "-m", "node", "-T", targetIQN, "-p", targetPortal, "--login").CombinedOutput()
	if err != nil {
		// Check if already logged in
		if !strings.Contains(string(output), "already present") {
			klog.Errorf("iSCSI login failed: %v, output: %s", err, string(output))
			return nil, status.Errorf(codes.Internal, "failed to login to iSCSI target: %v", err)
		}
	}
	klog.V(4).Infof("Successfully logged in to iSCSI target %s", targetIQN)

	devicePath, err := s.waitForDevice(targetIQN, lun)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find iSCSI device: %v", err)
	}

	fsType := "ext4"
	if req.VolumeCapability.GetMount() != nil {
		if req.VolumeCapability.GetMount().FsType != "" {
			fsType = req.VolumeCapability.GetMount().FsType
		}
	}

	formatted, err := s.isDeviceFormatted(devicePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check device format: %v", err)
	}

	if !formatted {
		klog.V(4).Infof("Formatting device %s with %s", devicePath, fsType)
		mkfsCmd := fmt.Sprintf("mkfs.%s", fsType)
		output, err = executor.Command(mkfsCmd, devicePath).CombinedOutput()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to format device: %v, output: %s", err, string(output))
		}
	}

	if err := os.MkdirAll(req.StagingTargetPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create staging directory: %v", err)
	}

	mountOptions := req.VolumeCapability.GetMount().GetMountFlags()
	err = s.mounter.Mount(devicePath, req.StagingTargetPath, fsType, mountOptions)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount device: %v", err)
	}

	klog.V(4).Infof("Successfully staged volume %s at %s", req.VolumeId, req.StagingTargetPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

func (s *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	klog.V(4).Infof("NodeUnstageVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	notMounted, err := s.mounter.IsLikelyNotMountPoint(req.StagingTargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("Staging path %s does not exist, considering unstaged", req.StagingTargetPath)
			return &csi.NodeUnstageVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to check mount point: %v", err)
	}

	if notMounted {
		klog.V(4).Infof("Volume %s is not mounted at %s", req.VolumeId, req.StagingTargetPath)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	err = s.mounter.Unmount(req.StagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount staging path: %v", err)
	}

	volInfo, _ := s.driver.getVolumeInfo(req.VolumeId)
	if volInfo != nil && volInfo.Protocol == "iscsi" {
		executor := exec.New()
		executor.Command("/usr/sbin/iscsiadm", "-m", "node", "-T", volInfo.TargetIQN, "--logout").CombinedOutput()
	}

	os.Remove(req.StagingTargetPath)

	klog.V(4).Infof("Successfully unstaged volume %s from %s", req.VolumeId, req.StagingTargetPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.V(4).Infof("NodePublishVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}

	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	notMounted, err := s.mounter.IsLikelyNotMountPoint(req.TargetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "failed to check mount point: %v", err)
		}
		if err := os.MkdirAll(req.TargetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create target directory: %v", err)
		}
		notMounted = true
	}

	if !notMounted {
		klog.V(4).Infof("Volume %s is already mounted at %s", req.VolumeId, req.TargetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err != nil {
		// Try to reconstruct from contexts
		volInfo = s.reconstructVolumeInfo(req.VolumeId, req.PublishContext, req.VolumeContext)
	}

	if volInfo.Protocol == "iscsi" {
		// For iSCSI, bind mount from staging path
		if req.StagingTargetPath == "" {
			return nil, status.Error(codes.InvalidArgument, "staging path is required for block volumes")
		}

		stagingNotMounted, err := s.mounter.IsLikelyNotMountPoint(req.StagingTargetPath)
		if err != nil || stagingNotMounted {
			return nil, status.Error(codes.FailedPrecondition, "volume not staged")
		}

		mountOptions := []string{"bind"}
		if req.Readonly {
			mountOptions = append(mountOptions, "ro")
		}

		err = s.mounter.Mount(req.StagingTargetPath, req.TargetPath, "", mountOptions)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
		}

	} else {
		// For NFS, mount directly
		nfsServer := req.PublishContext["nfsServer"]
		if nfsServer == "" {
			nfsServer = req.VolumeContext["nfsServer"]
		}
		nfsPath := req.PublishContext["nfsPath"]
		if nfsPath == "" {
			nfsPath = req.VolumeContext["nfsPath"]
		}

		if nfsServer == "" || nfsPath == "" {
			return nil, status.Error(codes.InvalidArgument, "NFS server and path are required")
		}

		source := fmt.Sprintf("%s:%s", nfsServer, nfsPath)

		mountOptions := []string{}
		if req.VolumeCapability.GetMount() != nil {
			mountOptions = req.VolumeCapability.GetMount().GetMountFlags()
		}
		if req.Readonly {
			mountOptions = append(mountOptions, "ro")
		}

		if len(mountOptions) == 0 {
			mountOptions = []string{"nfsvers=3", "nolock", "rsize=1048576", "wsize=1048576", "hard", "timeo=600", "retrans=2"}
		}

		err = s.mounter.Mount(source, req.TargetPath, NFS, mountOptions)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to mount NFS volume: %v", err)
		}
	}

	klog.V(4).Infof("Successfully published volume %s to %s", req.VolumeId, req.TargetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.V(4).Infof("NodeUnpublishVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}

	notMounted, err := s.mounter.IsLikelyNotMountPoint(req.TargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("Target path %s does not exist", req.TargetPath)
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to check mount point: %v", err)
	}

	if notMounted {
		klog.V(4).Infof("Volume %s is not mounted at %s", req.VolumeId, req.TargetPath)
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	err = s.mounter.Unmount(req.TargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount: %v", err)
	}

	os.Remove(req.TargetPath)

	klog.V(4).Infof("Successfully unpublished volume %s from %s", req.VolumeId, req.TargetPath)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (s *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(4).Infof("NodeGetInfo called")

	topology := &csi.Topology{
		Segments: map[string]string{
			TopologyKey: s.driver.nodeID,
		},
	}

	return &csi.NodeGetInfoResponse{
		NodeId:             s.driver.nodeID,
		MaxVolumesPerNode:  0, // No limit
		AccessibleTopology: topology,
	}, nil
}

func (s *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(4).Infof("NodeGetCapabilities called")

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: s.driver.nodeCaps,
	}, nil
}

func (s *NodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	klog.V(4).Infof("NodeGetVolumeStats called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	_, err := os.Stat(req.VolumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "volume path %s does not exist", req.VolumePath)
		}
		return nil, status.Errorf(codes.Internal, "failed to stat volume path: %v", err)
	}

	stats, err := s.getFSStats(req.VolumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get filesystem stats: %v", err)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: stats.availableBytes,
				Total:     stats.totalBytes,
				Used:      stats.usedBytes,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: stats.availableInodes,
				Total:     stats.totalInodes,
				Used:      stats.usedInodes,
			},
		},
		VolumeCondition: &csi.VolumeCondition{
			Abnormal: false,
			Message:  "Volume is healthy",
		},
	}, nil
}

func (s *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	klog.V(4).Infof("NodeExpandVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.VolumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err == nil && volInfo.Protocol == NFS {
		klog.V(4).Infof("Volume %s is NFS, no filesystem expansion needed", req.VolumeId)
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: req.CapacityRange.RequiredBytes,
		}, nil
	}

	if volInfo != nil && volInfo.Protocol == "iscsi" {
		devicePath, err := s.findDeviceForVolume(volInfo)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to find device: %v", err)
		}

		rescanPath := fmt.Sprintf("/sys/block/%s/device/rescan", filepath.Base(devicePath))
		if err := os.WriteFile(rescanPath, []byte("1\n"), 0200); err != nil {
			klog.Warningf("Failed to rescan device %s: %v", devicePath, err)
		}

		executor := exec.New()
		output, err := executor.Command("resize2fs", devicePath).CombinedOutput()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to resize filesystem: %v, output: %s", err, string(output))
		}
	}

	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: req.CapacityRange.RequiredBytes,
	}, nil
}

func (s *NodeServer) waitForDevice(targetIQN, lun string) (string, error) {
	executor := exec.New()

	// Give the device time to appear (max 30 seconds)
	for range 30 {
		sessionCmd := fmt.Sprintf("iscsiadm -m session -P 3 | grep -A 50 '%s'", targetIQN)
		output, _ := executor.Command("bash", "-c", sessionCmd).CombinedOutput()

		lines := strings.Split(string(output), "\n")
		for j, line := range lines {
			if strings.Contains(line, fmt.Sprintf("Lun: %s", lun)) {
				for k := j + 1; k < len(lines) && k < j+10; k++ {
					if strings.Contains(lines[k], "Attached scsi disk") {
						parts := strings.Fields(lines[k])
						if len(parts) > 3 {
							deviceName := parts[3]
							devicePath := filepath.Join("/dev", deviceName)

							if _, err := os.Stat(devicePath); err == nil {
								klog.V(4).Infof("Found device %s for target %s LUN %s", devicePath, targetIQN, lun)
								return devicePath, nil
							}
						}
					}
				}
			}
		}

		// Alternative method: check /dev/disk/by-path for the specific LUN
		byPathPattern := fmt.Sprintf("/dev/disk/by-path/*-%s-lun-%s", targetIQN, lun)
		matches, _ := filepath.Glob(byPathPattern)
		if len(matches) > 0 {
			// Resolve symlink to get actual device
			devicePath, err := filepath.EvalSymlinks(matches[0])
			if err == nil {
				klog.V(4).Infof("Found device %s via by-path for target %s LUN %s", devicePath, targetIQN, lun)
				return devicePath, nil
			}
		}

		// Wait 1 second before retry
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("device not found for target %s LUN %s after 30 seconds", targetIQN, lun)
}

func (s *NodeServer) isDeviceFormatted(devicePath string) (bool, error) {
	executor := exec.New()
	output, err := executor.Command("blkid", devicePath).CombinedOutput()
	if err != nil {
		// blkid returns non-zero if device has no recognized filesystem
		if strings.Contains(string(output), "UUID") || strings.Contains(string(output), "TYPE") {
			return true, nil
		}
		return false, nil
	}

	return true, nil
}

func (s *NodeServer) findDeviceForVolume(volInfo *VolumeInfo) (string, error) {
	return s.waitForDevice(volInfo.TargetIQN, fmt.Sprintf("%d", volInfo.LUN))
}

func (s *NodeServer) reconstructVolumeInfo(volumeID string, publishContext, volumeContext map[string]string) *VolumeInfo {
	volInfo := &VolumeInfo{
		ID:            volumeID,
		VolumeContext: volumeContext,
	}

	if _, ok := publishContext["targetIQN"]; ok {
		volInfo.Protocol = ISCSI
		volInfo.TargetIQN = publishContext["targetIQN"]
		volInfo.TargetPortal = publishContext["targetPortal"]
	} else {
		volInfo.Protocol = NFS
		volInfo.NFSPath = publishContext["nfsPath"]
		if volInfo.NFSPath == "" {
			volInfo.NFSPath = volumeContext["nfsPath"]
		}
	}

	return volInfo
}

type fsStats struct {
	totalBytes      int64
	availableBytes  int64
	usedBytes       int64
	totalInodes     int64
	availableInodes int64
	usedInodes      int64
}

func (s *NodeServer) getFSStats(path string) (*fsStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, fmt.Errorf("statfs failed: %v", err)
	}

	blockSize := stat.Frsize
	if blockSize == 0 {
		blockSize = stat.Bsize
	}

	totalBytes := int64(stat.Blocks) * blockSize
	availableBytes := int64(stat.Bavail) * blockSize
	freeBytes := int64(stat.Bfree) * blockSize
	usedBytes := totalBytes - freeBytes

	totalInodes := int64(stat.Files)
	availableInodes := int64(stat.Ffree)
	usedInodes := totalInodes - availableInodes

	return &fsStats{
		totalBytes:      totalBytes,
		availableBytes:  availableBytes,
		usedBytes:       usedBytes,
		totalInodes:     totalInodes,
		availableInodes: availableInodes,
		usedInodes:      usedInodes,
	}, nil
}
