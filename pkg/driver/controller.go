package driver

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/iXsystems/truenas_k8_driver/pkg/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

const (
	ISCSI              = "iscsi"
	NFS                = "nfs"
	LZ4                = "LZ4"
	DEFAULT_MOUNTPOINT = "/mnt"
)

type ControllerServer struct {
	driver *Driver
	csi.UnimplementedControllerServer
}

func NewControllerServer(driver *Driver) *ControllerServer {
	return &ControllerServer{
		driver: driver,
	}
}

func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("CreateVolume called with request: %+v", req)

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}

	if len(req.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	var requiredBytes int64
	if req.CapacityRange != nil {
		requiredBytes = req.CapacityRange.RequiredBytes
		if requiredBytes < 0 {
			return nil, status.Error(codes.InvalidArgument, "required bytes cannot be negative")
		}
	}

	parameters := req.Parameters
	if parameters == nil {
		parameters = make(map[string]string)
	}

	protocol := s.driver.getProtocolFromParameters(parameters)
	pool := s.driver.getPoolFromParameters(parameters)

	volumeName := sanitizeVolumeName(req.Name)
	volumeID := s.driver.generateVolumeID(pool, volumeName)
	datasetPath := pool + "/" + volumeName

	existingDataset, err := s.driver.client.GetDataset(ctx, datasetPath)
	if err == nil && existingDataset != nil {
		if existingDataset.RefQuota > 0 && existingDataset.RefQuota < requiredBytes {
			return nil, status.Error(codes.AlreadyExists, "volume exists but with insufficient capacity")
		}

		volInfo := &VolumeInfo{
			ID:            volumeID,
			Name:          volumeName,
			CapacityBytes: existingDataset.RefQuota,
			DatasetPath:   datasetPath,
			PoolName:      pool,
			Protocol:      protocol,
			VolumeContext: parameters,
		}

		s.driver.storeVolumeInfo(volInfo)

		return &csi.CreateVolumeResponse{
			Volume: &csi.Volume{
				VolumeId:      volumeID,
				CapacityBytes: existingDataset.RefQuota,
				VolumeContext: parameters,
			},
		}, nil
	}

	if req.VolumeContentSource != nil {
		return s.createVolumeFromSource(ctx, req, volumeID, datasetPath, protocol, parameters)
	}

	var volInfo *VolumeInfo
	if protocol == ISCSI {
		volInfo, err = s.createISCSIVolume(ctx, volumeID, datasetPath, requiredBytes, parameters)
	} else {
		volInfo, err = s.createNFSVolume(ctx, volumeID, datasetPath, requiredBytes, parameters)
	}

	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create volume: %v", err)
	}

	s.driver.storeVolumeInfo(volInfo)

	resp := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volInfo.ID,
			CapacityBytes: volInfo.CapacityBytes,
			VolumeContext: volInfo.VolumeContext,
			ContentSource: volInfo.ContentSource,
		},
	}

	if volInfo.AccessibleTopology != nil {
		resp.Volume.AccessibleTopology = volInfo.AccessibleTopology
	}

	klog.V(4).Infof("Volume created successfully: %s", volumeID)
	return resp, nil
}

func (s *ControllerServer) createNFSVolume(ctx context.Context, volumeID, datasetPath string, capacityBytes int64, parameters map[string]string) (*VolumeInfo, error) {
	compression := LZ4
	if val, ok := parameters["compression"]; ok {
		compression = strings.ToUpper(val)
	}

	sync := "STANDARD"
	if val, ok := parameters["sync"]; ok {
		sync = strings.ToUpper(val)
	}

	datasetOpts := &client.DatasetCreateOptions{
		Name:        datasetPath,
		Type:        "FILESYSTEM",
		RefQuota:    capacityBytes,
		Compression: compression,
		Sync:        sync,
		Properties:  make(map[string]any),
	}

	for key, value := range parameters {
		if propName, found := strings.CutPrefix(key, "zfs."); found {
			datasetOpts.Properties[propName] = value
		}
	}

	dataset, err := s.driver.client.CreateDataset(ctx, datasetOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataset: %w", err)
	}

	mountpoint := dataset.Mountpoint
	if mountpoint == "" {
		mountpoint = filepath.Join(DEFAULT_MOUNTPOINT, datasetPath)
	}

	stringPtr := func(s string) *string { return &s }

	shareOpts := &client.NFSShareCreateOptions{
		Path:        mountpoint,
		Comment:     fmt.Sprintf("CSI volume %s", volumeID),
		Enabled:     true,
		ReadOnly:    false,
		MapAllUser:  stringPtr("root"),
		MapAllGroup: stringPtr("wheel"),
	}

	// Set hosts - if not specified, don't set it (TrueNAS will allow all)
	if hosts, ok := parameters["nfs.hosts"]; ok {
		shareOpts.Hosts = strings.Split(hosts, ",")
	}

	if networks, ok := parameters["nfs.networks"]; ok {
		shareOpts.Networks = strings.Split(networks, ",")
	}

	klog.V(4).Infof("Creating NFS share for %s with hosts: %v, networks: %v", mountpoint, shareOpts.Hosts, shareOpts.Networks)
	share, err := s.driver.client.CreateNFSShare(ctx, shareOpts)
	if err != nil {
		klog.Errorf("Failed to create NFS share for %s: %v", mountpoint, err)
		// Cleanup dataset on failure
		s.driver.client.DeleteDataset(ctx, datasetPath)
		return nil, fmt.Errorf("failed to create NFS share: %w", err)
	}
	klog.V(2).Infof("Successfully created NFS share ID %d for path %s", share.ID, mountpoint)

	volInfo := &VolumeInfo{
		ID:            volumeID,
		Name:          volumeID,
		CapacityBytes: capacityBytes,
		DatasetPath:   datasetPath,
		PoolName:      client.ExtractPoolFromPath(datasetPath),
		Protocol:      "nfs",
		NFSPath:       mountpoint,
		NFSShareID:    share.ID,
		VolumeContext: parameters,
	}

	if s.driver.nfsServer != "" {
		volInfo.VolumeContext["nfsServer"] = s.driver.nfsServer
	}
	volInfo.VolumeContext["nfsPath"] = mountpoint

	return volInfo, nil
}

func (s *ControllerServer) createISCSIVolume(ctx context.Context, volumeID, datasetPath string, capacityBytes int64, parameters map[string]string) (*VolumeInfo, error) {
	compression := "LZ4"
	if val, ok := parameters["compression"]; ok {
		compression = strings.ToUpper(val)
	}

	volblocksize := "16K" // default
	if val, ok := parameters["volblocksize"]; ok {
		volblocksize = val
	}

	datasetOpts := &client.DatasetCreateOptions{
		Name:         datasetPath,
		Type:         "VOLUME",
		Volsize:      capacityBytes,
		Volblocksize: volblocksize,
		Compression:  compression,
		Properties:   make(map[string]any),
	}

	for key, value := range parameters {
		if propName, found := strings.CutPrefix(key, "zfs."); found {
			datasetOpts.Properties[propName] = value
		}
	}

	_, err := s.driver.client.CreateDataset(ctx, datasetOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create ZVOL: %w", err)
	}

	// Get IQN base: prefer StorageClass override, then auto-detect from TrueNAS,
	// then fall back to the driver-level default.
	iqnBase := s.driver.getISCSIIQNBaseFromParameters(parameters)
	if iqnBase == s.driver.iscsiIQNBase {
		// No per-StorageClass override — try to read the real basename from TrueNAS
		if detected, err := s.driver.client.GetISCSIBasename(ctx); err == nil {
			iqnBase = detected
		}
	}

	targetSuffix := fmt.Sprintf("csi-%s", strings.ReplaceAll(volumeID, "/", "-"))
	target, err := s.driver.client.CreateISCSITarget(ctx, targetSuffix, fmt.Sprintf("CSI volume %s", volumeID))
	if err != nil {
		// Cleanup ZVOL on failure
		s.driver.client.DeleteDataset(ctx, datasetPath)
		return nil, fmt.Errorf("failed to create iSCSI target: %w", err)
	}

	zvolPath := fmt.Sprintf("zvol/%s", datasetPath)
	blocksize := 512 // default
	if val, ok := parameters["iscsi.blocksize"]; ok {
		if bs, err := strconv.Atoi(val); err == nil {
			blocksize = bs
		}
	}

	extent, err := s.driver.client.CreateISCSIExtent(ctx, volumeID, zvolPath, blocksize)
	if err != nil {
		// Cleanup
		s.driver.client.DeleteISCSITarget(ctx, target.ID)
		s.driver.client.DeleteDataset(ctx, datasetPath)
		return nil, fmt.Errorf("failed to create iSCSI extent: %w", err)
	}

	_, err = s.driver.client.CreateISCSITargetExtent(ctx, target.ID, extent.ID, 0)
	if err != nil {
		// Cleanup
		s.driver.client.DeleteISCSIExtent(ctx, extent.ID)
		s.driver.client.DeleteISCSITarget(ctx, target.ID)
		s.driver.client.DeleteDataset(ctx, datasetPath)
		return nil, fmt.Errorf("failed to associate target and extent: %w", err)
	}

	fullIQN := fmt.Sprintf("%s:%s", iqnBase, target.Name)

	volInfo := &VolumeInfo{
		ID:            volumeID,
		Name:          volumeID,
		CapacityBytes: capacityBytes,
		DatasetPath:   datasetPath,
		PoolName:      client.ExtractPoolFromPath(datasetPath),
		Protocol:      "iscsi",
		TargetIQN:     fullIQN,
		TargetPortal:  s.driver.iscsiPortal,
		LUN:           0,
		ISCSITargetID: target.ID,
		ISCSIExtentID: extent.ID,
		VolumeContext: parameters,
	}

	volInfo.VolumeContext["targetPortal"] = s.driver.iscsiPortal
	volInfo.VolumeContext["targetIQN"] = fullIQN
	volInfo.VolumeContext["lun"] = "0"

	return volInfo, nil
}

func (s *ControllerServer) createVolumeFromSource(ctx context.Context, req *csi.CreateVolumeRequest, volumeID, datasetPath, protocol string, parameters map[string]string) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("createVolumeFromSource: Creating volume %s from content source", volumeID)
	contentSource := req.VolumeContentSource

	switch {
	case contentSource.GetSnapshot() != nil:
		// Create volume from snapshot
		klog.V(4).Infof("createVolumeFromSource: Creating volume %s from snapshot", volumeID)
		snapshot := contentSource.GetSnapshot()
		if snapshot.SnapshotId == "" {
			return nil, status.Error(codes.InvalidArgument, "snapshot ID is required")
		}

		klog.V(4).Infof("createVolumeFromSource: Cloning snapshot %s to %s", snapshot.SnapshotId, datasetPath)
		_, err := s.driver.client.CloneSnapshot(ctx, snapshot.SnapshotId, datasetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to clone snapshot: %v", err)
		}

		// Set capacity on the cloned dataset to match requested size
		// Use volsize for iSCSI (ZVOL) or refquota for NFS (filesystem)
		requiredBytes := req.CapacityRange.RequiredBytes
		if requiredBytes > 0 {
			updateOpts := &client.DatasetUpdateOptions{}
			if protocol == ISCSI {
				klog.V(4).Infof("createVolumeFromSource: Setting volsize and refreservation to %d bytes on restored ZVOL %s", requiredBytes, datasetPath)
				updateOpts.Volsize = &requiredBytes
				updateOpts.RefReservation = &requiredBytes // Make it thick provisioned like the original
			} else {
				klog.V(4).Infof("createVolumeFromSource: Setting refquota to %d bytes on restored dataset %s", requiredBytes, datasetPath)
				updateOpts.RefQuota = &requiredBytes
			}
			err = s.driver.client.UpdateDataset(ctx, datasetPath, updateOpts)
			if err != nil {
				klog.Errorf("createVolumeFromSource: Failed to set capacity on restored dataset %s: %v", datasetPath, err)
				return nil, status.Errorf(codes.Internal, "failed to set capacity on restored volume: %v", err)
			}
			klog.V(4).Infof("createVolumeFromSource: Successfully set capacity on restored dataset %s", datasetPath)
		}

		dataset, err := s.driver.client.GetDataset(ctx, datasetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get cloned dataset: %v", err)
		}

		var volInfo *VolumeInfo
		if protocol == ISCSI {
			volInfo, err = s.createISCSITargetForClone(ctx, volumeID, datasetPath, requiredBytes, parameters)
		} else {
			volInfo, err = s.createNFSShareForClone(ctx, volumeID, datasetPath, dataset, parameters)
		}

		if err != nil {
			s.driver.client.DeleteDataset(ctx, datasetPath)
			return nil, status.Errorf(codes.Internal, "failed to create share for clone: %v", err)
		}

		volInfo.ContentSource = contentSource
		s.driver.storeVolumeInfo(volInfo)

		capacityBytes := dataset.RefQuota
		if protocol == ISCSI {
			capacityBytes = requiredBytes
		}

		return &csi.CreateVolumeResponse{
			Volume: &csi.Volume{
				VolumeId:      volumeID,
				CapacityBytes: capacityBytes,
				VolumeContext: parameters,
				ContentSource: contentSource,
			},
		}, nil

	case contentSource.GetVolume() != nil:
		klog.V(4).Infof("createVolumeFromSource: Creating volume %s by cloning from volume", volumeID)
		sourceVolume := contentSource.GetVolume()
		if sourceVolume.VolumeId == "" {
			return nil, status.Error(codes.InvalidArgument, "source volume ID is required")
		}

		klog.V(4).Infof("createVolumeFromSource: Getting info for source volume %s", sourceVolume.VolumeId)
		sourceInfo, err := s.driver.getVolumeInfo(sourceVolume.VolumeId)
		if err != nil {
			klog.Errorf("createVolumeFromSource: Failed to get source volume info for %s: %v", sourceVolume.VolumeId, err)
			return nil, status.Errorf(codes.NotFound, "source volume not found: %v", err)
		}

		sanitizedVolumeID := strings.ReplaceAll(volumeID, "/", "-")
		snapshotName := fmt.Sprintf("csi-clone-%s-%d", sanitizedVolumeID, time.Now().Unix())
		klog.V(4).Infof("createVolumeFromSource: Creating temporary snapshot %s of dataset %s", snapshotName, sourceInfo.DatasetPath)
		snapshot, err := s.driver.client.CreateSnapshot(ctx, sourceInfo.DatasetPath, snapshotName, false)
		if err != nil {
			klog.Errorf("createVolumeFromSource: Failed to create snapshot %s: %v", snapshotName, err)
			return nil, status.Errorf(codes.Internal, "failed to create snapshot for clone: %v", err)
		}
		klog.V(4).Infof("createVolumeFromSource: Created temporary snapshot %s", snapshot.ID)

		klog.V(4).Infof("createVolumeFromSource: Cloning snapshot %s to new dataset %s", snapshot.ID, datasetPath)
		_, err = s.driver.client.CloneSnapshot(ctx, snapshot.ID, datasetPath)
		if err != nil {
			klog.Errorf("createVolumeFromSource: Failed to clone snapshot %s to %s: %v", snapshot.ID, datasetPath, err)
			s.driver.client.DeleteSnapshot(ctx, snapshot.ID)
			return nil, status.Errorf(codes.Internal, "failed to clone volume: %v", err)
		}
		klog.V(4).Infof("createVolumeFromSource: Successfully cloned to dataset %s", datasetPath)

		klog.V(4).Infof("createVolumeFromSource: Deleting temporary snapshot %s", snapshot.ID)
		s.driver.client.DeleteSnapshot(ctx, snapshot.ID)
		klog.V(4).Infof("createVolumeFromSource: Deleted temporary snapshot %s", snapshot.ID)

		// Set capacity on the cloned dataset to match requested size
		// Use volsize for iSCSI (ZVOL) or refquota for NFS (filesystem)
		requiredBytes := req.CapacityRange.RequiredBytes
		if requiredBytes > 0 {
			updateOpts := &client.DatasetUpdateOptions{}
			if protocol == ISCSI {
				klog.V(4).Infof("createVolumeFromSource: Setting volsize and refreservation to %d bytes on cloned ZVOL %s", requiredBytes, datasetPath)
				updateOpts.Volsize = &requiredBytes
				updateOpts.RefReservation = &requiredBytes // Make it thick provisioned like the original
			} else {
				klog.V(4).Infof("createVolumeFromSource: Setting refquota to %d bytes on cloned dataset %s", requiredBytes, datasetPath)
				updateOpts.RefQuota = &requiredBytes
			}
			err = s.driver.client.UpdateDataset(ctx, datasetPath, updateOpts)
			if err != nil {
				klog.Errorf("createVolumeFromSource: Failed to set capacity on cloned dataset %s: %v", datasetPath, err)
				return nil, status.Errorf(codes.Internal, "failed to set capacity on cloned volume: %v", err)
			}
			klog.V(4).Infof("createVolumeFromSource: Successfully set capacity on cloned dataset %s", datasetPath)
		}

		dataset, err := s.driver.client.GetDataset(ctx, datasetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get cloned dataset: %v", err)
		}

		var volInfo *VolumeInfo
		if protocol == "iscsi" {
			volInfo, err = s.createISCSITargetForClone(ctx, volumeID, datasetPath, requiredBytes, parameters)
		} else {
			volInfo, err = s.createNFSShareForClone(ctx, volumeID, datasetPath, dataset, parameters)
		}

		if err != nil {
			s.driver.client.DeleteDataset(ctx, datasetPath)
			return nil, status.Errorf(codes.Internal, "failed to create share for clone: %v", err)
		}

		volInfo.ContentSource = contentSource
		s.driver.storeVolumeInfo(volInfo)

		capacityBytes := dataset.RefQuota
		if protocol == ISCSI {
			// For ZVOLs, use the volsize we just set
			capacityBytes = requiredBytes
		}

		return &csi.CreateVolumeResponse{
			Volume: &csi.Volume{
				VolumeId:      volumeID,
				CapacityBytes: capacityBytes,
				VolumeContext: parameters,
				ContentSource: contentSource,
			},
		}, nil

	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported volume content source")
	}
}

func (s *ControllerServer) createNFSShareForClone(ctx context.Context, volumeID, datasetPath string, dataset *client.Dataset, parameters map[string]string) (*VolumeInfo, error) {
	mountpoint := dataset.Mountpoint
	if mountpoint == "" {
		mountpoint = fmt.Sprintf("/mnt/%s", datasetPath)
	}

	shareOpts := &client.NFSShareCreateOptions{
		Path:     mountpoint,
		Comment:  fmt.Sprintf("CSI volume clone %s", volumeID),
		Enabled:  true,
		ReadOnly: false,
	}

	share, err := s.driver.client.CreateNFSShare(ctx, shareOpts)
	if err != nil {
		return nil, err
	}

	volInfo := &VolumeInfo{
		ID:            volumeID,
		Name:          volumeID,
		CapacityBytes: dataset.RefQuota,
		DatasetPath:   datasetPath,
		PoolName:      client.ExtractPoolFromPath(datasetPath),
		Protocol:      "nfs",
		NFSPath:       mountpoint,
		NFSShareID:    share.ID,
		VolumeContext: parameters,
	}

	if s.driver.nfsServer != "" {
		volInfo.VolumeContext["nfsServer"] = s.driver.nfsServer
	}
	volInfo.VolumeContext["nfsPath"] = mountpoint

	return volInfo, nil
}

func (s *ControllerServer) createISCSITargetForClone(ctx context.Context, volumeID, datasetPath string, capacityBytes int64, parameters map[string]string) (*VolumeInfo, error) {
	iqnBase := s.driver.getISCSIIQNBaseFromParameters(parameters)
	if iqnBase == s.driver.iscsiIQNBase {
		if detected, err := s.driver.client.GetISCSIBasename(ctx); err == nil {
			iqnBase = detected
		}
	}

	// Replace slash in volumeID for valid iSCSI target name
	targetSuffix := fmt.Sprintf("csi-%s", strings.ReplaceAll(volumeID, "/", "-"))
	target, err := s.driver.client.CreateISCSITarget(ctx, targetSuffix, fmt.Sprintf("CSI volume clone %s", volumeID))
	if err != nil {
		return nil, err
	}

	zvolPath := fmt.Sprintf("zvol/%s", datasetPath)
	extent, err := s.driver.client.CreateISCSIExtent(ctx, volumeID, zvolPath, 512)
	if err != nil {
		s.driver.client.DeleteISCSITarget(ctx, target.ID)
		return nil, err
	}

	_, err = s.driver.client.CreateISCSITargetExtent(ctx, target.ID, extent.ID, 0)
	if err != nil {
		s.driver.client.DeleteISCSIExtent(ctx, extent.ID)
		s.driver.client.DeleteISCSITarget(ctx, target.ID)
		return nil, err
	}

	fullIQN := fmt.Sprintf("%s:%s", iqnBase, target.Name)

	volInfo := &VolumeInfo{
		ID:            volumeID,
		Name:          volumeID,
		CapacityBytes: capacityBytes,
		DatasetPath:   datasetPath,
		PoolName:      client.ExtractPoolFromPath(datasetPath),
		Protocol:      "iscsi",
		TargetIQN:     fullIQN,
		TargetPortal:  s.driver.iscsiPortal,
		LUN:           0,
		ISCSITargetID: target.ID,
		ISCSIExtentID: extent.ID,
		VolumeContext: parameters,
	}

	volInfo.VolumeContext["targetPortal"] = s.driver.iscsiPortal
	volInfo.VolumeContext["targetIQN"] = fullIQN
	volInfo.VolumeContext["lun"] = "0"

	return volInfo, nil
}

func (s *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("DeleteVolume called with volume ID: %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	pool, name, err := s.driver.parseVolumeID(req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	datasetPath := fmt.Sprintf("%s/%s", pool, name)

	volInfo, _ := s.driver.getVolumeInfo(req.VolumeId)

	if volInfo != nil {
		if volInfo.Protocol == "iscsi" {
			if volInfo.ISCSITargetID > 0 {
				s.driver.client.DeleteISCSITarget(ctx, volInfo.ISCSITargetID)
			}
			if volInfo.ISCSIExtentID > 0 {
				s.driver.client.DeleteISCSIExtent(ctx, volInfo.ISCSIExtentID)
			}
		} else {
			if volInfo.NFSShareID > 0 {
				s.driver.client.DeleteNFSShare(ctx, volInfo.NFSShareID)
			}
		}
	}

	err = s.driver.client.DeleteDataset(ctx, datasetPath)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			klog.V(4).Infof("Volume %s already deleted", req.VolumeId)
			s.driver.deleteVolumeInfo(req.VolumeId)
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to delete volume: %v", err)
	}

	s.driver.deleteVolumeInfo(req.VolumeId)

	klog.V(4).Infof("Volume %s deleted successfully", req.VolumeId)
	return &csi.DeleteVolumeResponse{}, nil
}

func (s *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerPublishVolume called for volume %s on node %s", req.VolumeId, req.NodeId)

	// For NFS, this is a no-op - volume attributes are already in VolumeContext
	// For iSCSI, we might need to configure initiator access here

	// Try to get volume info from memory, but don't fail if not found
	// (controller might have restarted and lost in-memory state)
	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err != nil {
		// Volume not in memory - for NFS this is OK, just return empty publish context
		// The volume attributes are already stored in the PV's VolumeContext
		klog.V(4).Infof("Volume %s not found in memory (controller may have restarted), returning empty publish context", req.VolumeId)
		return &csi.ControllerPublishVolumeResponse{
			PublishContext: make(map[string]string),
		}, nil
	}

	publishContext := make(map[string]string)

	if volInfo.Protocol == ISCSI {
		publishContext["targetPortal"] = volInfo.TargetPortal
		publishContext["targetIQN"] = volInfo.TargetIQN
		publishContext["lun"] = fmt.Sprintf("%d", volInfo.LUN)
	} else {
		publishContext["nfsServer"] = volInfo.VolumeContext["nfsServer"]
		publishContext["nfsPath"] = volInfo.NFSPath
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: publishContext,
	}, nil
}

func (s *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called for volume %s on node %s", req.VolumeId, req.NodeId)

	// For NFS, this is a no-op
	// For iSCSI, we might need to remove initiator access here

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (s *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	klog.V(4).Infof("ValidateVolumeCapabilities called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if len(req.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	pool, name, err := s.driver.parseVolumeID(req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	datasetPath := fmt.Sprintf("%s/%s", pool, name)
	_, err = s.driver.client.GetDataset(ctx, datasetPath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.VolumeContext,
			VolumeCapabilities: req.VolumeCapabilities,
			Parameters:         req.Parameters,
		},
	}, nil
}

func (s *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Infof("ListVolumes called")

	s.driver.volumesMutex.RLock()
	defer s.driver.volumesMutex.RUnlock()

	entries := make([]*csi.ListVolumesResponse_Entry, 0, len(s.driver.volumes))
	for _, vol := range s.driver.volumes {
		entry := &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      vol.ID,
				CapacityBytes: vol.CapacityBytes,
				VolumeContext: vol.VolumeContext,
			},
			Status: &csi.ListVolumesResponse_VolumeStatus{
				VolumeCondition: &csi.VolumeCondition{
					Abnormal: false,
					Message:  "Volume is healthy",
				},
			},
		}
		entries = append(entries, entry)
	}

	return &csi.ListVolumesResponse{
		Entries: entries,
	}, nil
}

func (s *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Infof("GetCapacity called")

	pool := s.driver.defaultPool
	if req.Parameters != nil {
		if p, ok := req.Parameters["pool"]; ok {
			pool = p
		}
	}

	available, err := s.driver.client.GetAvailableSpace(ctx, pool)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get capacity: %v", err)
	}

	return &csi.GetCapacityResponse{
		AvailableCapacity: available,
	}, nil
}

func (s *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	klog.V(4).Infof("ControllerGetCapabilities called")

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.driver.controllerCaps,
	}, nil
}

func (s *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	klog.V(4).Infof("CreateSnapshot called for volume %s", req.SourceVolumeId)

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot name is required")
	}

	if req.SourceVolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "source volume ID is required")
	}

	volInfo, err := s.driver.getVolumeInfo(req.SourceVolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
	}

	snapshotName := sanitizeVolumeName(req.Name)
	snapshot, err := s.driver.client.CreateSnapshot(ctx, volInfo.DatasetPath, snapshotName, false)
	if err != nil {
		// Check if snapshot already exists
		if strings.Contains(err.Error(), "already exists") {
			snapshots, _ := s.driver.client.ListSnapshots(ctx, volInfo.DatasetPath)
			for _, snap := range snapshots {
				if strings.Contains(snap.Name, snapshotName) {
					return &csi.CreateSnapshotResponse{
						Snapshot: &csi.Snapshot{
							SnapshotId:     snap.ID,
							SourceVolumeId: req.SourceVolumeId,
							CreationTime:   timestamppb.Now(),
							ReadyToUse:     true,
						},
					}, nil
				}
			}
		}
		return nil, status.Errorf(codes.Internal, "failed to create snapshot: %v", err)
	}

	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snapshot.ID,
			SourceVolumeId: req.SourceVolumeId,
			CreationTime:   timestamppb.Now(),
			ReadyToUse:     true,
		},
	}, nil
}

func (s *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	klog.V(4).Infof("DeleteSnapshot called for snapshot %s", req.SnapshotId)

	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot ID is required")
	}

	err := s.driver.client.DeleteSnapshot(ctx, req.SnapshotId)
	if err != nil {
		// If snapshot doesn't exist, consider it deleted
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			return &csi.DeleteSnapshotResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to delete snapshot: %v", err)
	}

	return &csi.DeleteSnapshotResponse{}, nil
}

func (s *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots called")

	entries := []*csi.ListSnapshotsResponse_Entry{}

	if req.SourceVolumeId != "" {
		volInfo, err := s.driver.getVolumeInfo(req.SourceVolumeId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
		}

		snapshots, err := s.driver.client.ListSnapshots(ctx, volInfo.DatasetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
		}

		for _, snap := range snapshots {
			if req.SnapshotId != "" && snap.ID != req.SnapshotId {
				continue
			}

			entry := &csi.ListSnapshotsResponse_Entry{
				Snapshot: &csi.Snapshot{
					SnapshotId:     snap.ID,
					SourceVolumeId: req.SourceVolumeId,
					CreationTime:   timestamppb.Now(),
					ReadyToUse:     true,
				},
			}
			entries = append(entries, entry)
		}
	}

	return &csi.ListSnapshotsResponse{
		Entries: entries,
	}, nil
}

func (s *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).Infof("ControllerExpandVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.CapacityRange == nil || req.CapacityRange.RequiredBytes == 0 {
		return nil, status.Error(codes.InvalidArgument, "capacity range is required")
	}

	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
	}

	newSize := req.CapacityRange.RequiredBytes
	if newSize <= volInfo.CapacityBytes {
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         volInfo.CapacityBytes,
			NodeExpansionRequired: false,
		}, nil
	}

	updates := &client.DatasetUpdateOptions{}
	if volInfo.Protocol == "iscsi" {
		updates.Volsize = &newSize
	} else {
		updates.RefQuota = &newSize
	}

	err = s.driver.client.UpdateDataset(ctx, volInfo.DatasetPath, updates)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to expand volume: %v", err)
	}

	volInfo.CapacityBytes = newSize
	s.driver.storeVolumeInfo(volInfo)

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         newSize,
		NodeExpansionRequired: volInfo.Protocol == "iscsi", // Only iSCSI needs node-side filesystem resize
	}, nil
}

func (s *ControllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("ControllerGetVolume called for volume %s", req.VolumeId)

	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	volInfo, err := s.driver.getVolumeInfo(req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume not found: %v", err)
	}

	dataset, err := s.driver.client.GetDataset(ctx, volInfo.DatasetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get volume info: %v", err)
	}

	abnormal := false
	message := "Volume is healthy"

	if dataset.Used > dataset.Available {
		abnormal = true
		message = "Volume is running out of space"
	}

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volInfo.ID,
			CapacityBytes: volInfo.CapacityBytes,
			VolumeContext: volInfo.VolumeContext,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

func (s *ControllerServer) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	klog.V(4).Infof("ControllerModifyVolume called for volume %s", req.VolumeId)

	return &csi.ControllerModifyVolumeResponse{}, nil
}
