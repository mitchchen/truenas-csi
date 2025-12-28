package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	CREATE_DATASET string = "pool.dataset.create"
	GET_DATASET    string = "pool.dataset.get_instance"
	DELETE_DATASET string = "pool.dataset.delete"
	UPDATE_DATASET string = "pool.dataset.update"
)

const (
	CREATE_NFS_SHARE string = "sharing.nfs.create"
	GET_NFS_SHARE    string = "sharing.nfs.get_instance"
	QUERY_NFS_SHARE  string = "sharing.nfs.query"
	DELETE_NFS_SHARE string = "sharing.nfs.delete"
)

const (
	CREATE_ISCSI_TARGET        string = "iscsi.target.create"
	CREATE_ISCSI_EXTENT        string = "iscsi.extent.create"
	CREATE_ISCSI_TARGET_EXTENT string = "iscsi.targetextent.create"
	QUERY_ISCSI_TARGET_EXTENT  string = "iscsi.targetextent.query"
	DELETE_ISCSI_TARGET_EXTENT string = "iscsi.targetextent.delete"
	DELETE_ISCSI_TARGET        string = "iscsi.target.delete"
	DELETE_ISCSI_EXTENT        string = "iscsi.extent.delete"
)

const (
	CREATE_SNAPSHOT string = "pool.snapshot.create"
	DELETE_SNAPSHOT string = "pool.snapshot.delete"
	CLONE_SNAPSHOT  string = "pool.snapshot.clone"
	QUERY_SNAPSHOT  string = "pool.snapshot.query"
)

const (
	POOL_CREATE string = "pool.create"
	POOL_QUERY  string = "pool.query"
	POOL_REMOVE string = "pool.remove"
	POOL_EXPORT string = "pool.export"
)

const (
	ZFS_RESOURCE_QUERY string = "zfs.resource.query"
)

type Dataset struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Pool            string         `json:"pool"`
	Type            string         `json:"type"`
	Mountpoint      string         `json:"mountpoint"`
	Used            int64          `json:"used"`
	Available       int64          `json:"available"`
	RefQuota        int64          `json:"refquota"`
	RefReservation  int64          `json:"refreservation"`
	Compression     any            `json:"compression"`   // Can be string or object in TrueNAS
	Deduplication   any            `json:"deduplication"` // Can be string or object in TrueNAS
	Sync            any            `json:"sync"`          // Can be string or object in TrueNAS
	RecordSize      any            `json:"recordsize"`    // Can be string or object in TrueNAS
	ACLMode         any            `json:"aclmode"`       // Can be string or object in TrueNAS
	ACLType         any            `json:"acltype"`       // Can be string or object in TrueNAS
	ExtraProperties map[string]any `json:"extra_properties,omitempty"`
}

type DatasetCreateOptions struct {
	Name            string         `json:"name"`
	Type            string         `json:"type,omitempty"` // FILESYSTEM or VOLUME
	RefQuota        int64          `json:"refquota,omitempty"`
	RefReservation  int64          `json:"refreservation,omitempty"`
	Quota           int64          `json:"quota,omitempty"`
	Reservation     int64          `json:"reservation,omitempty"`
	Compression     string         `json:"compression,omitempty"`
	Deduplication   string         `json:"deduplication,omitempty"`
	Sync            string         `json:"sync,omitempty"`
	RecordSize      string         `json:"recordsize,omitempty"`
	Volsize         int64          `json:"volsize,omitempty"` // For ZVOLs
	Volblocksize    string         `json:"volblocksize,omitempty"`
	Comments        string         `json:"comments,omitempty"`
	CreateAncestors bool           `json:"create_ancestors,omitempty"`
	Properties      map[string]any `json:"properties,omitempty"`
}

type DatasetUpdateOptions struct {
	Comments         string              `json:"comments,omitempty"`
	Sync             string              `json:"sync,omitempty"`
	Compression      string              `json:"compression,omitempty"`
	Exec             string              `json:"exec,omitempty"`
	Quota            *int64              `json:"quota,omitempty"`
	RefQuota         *int64              `json:"refquota,omitempty"`
	Reservation      *int64              `json:"reservation,omitempty"`
	RefReservation   *int64              `json:"refreservation,omitempty"`
	Checksum         string              `json:"checksum,omitempty"`
	Deduplication    string              `json:"deduplication,omitempty"`
	Readonly         string              `json:"readonly,omitempty"`
	Atime            string              `json:"atime,omitempty"`
	RecordSize       string              `json:"recordsize,omitempty"`
	Volsize          *int64              `json:"volsize,omitempty"`
	QuotaWarning     *int64              `json:"quota_warning,omitempty"`
	QuotaCritical    *int64              `json:"quota_critical,omitempty"`
	RefQuotaWarning  *int64              `json:"refquota_warning,omitempty"`
	RefQuotaCritical *int64              `json:"refquota_critical,omitempty"`
	UserProperties   []map[string]string `json:"user_properties,omitempty"`
}

// QueryOptions represents standard TrueNAS query options used by all .query and .get_instance methods
type QueryOptions struct {
	Select          []string       `json:"select,omitempty"`
	OrderBy         []string       `json:"order_by,omitempty"`
	Count           bool           `json:"count,omitempty"`
	Get             bool           `json:"get,omitempty"`
	Offset          int            `json:"offset,omitempty"`
	Limit           int            `json:"limit,omitempty"`
	ForceSQLFilters bool           `json:"force_sql_filters,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

type DatasetQueryOptions struct {
	Extra DatasetGetExtraOptions `json:"extra"`
}

type DatasetGetExtraOptions struct {
	Properties []string `json:"properties"`
}

type DatasetDeleteOptions struct {
	Recursive bool `json:"recursive"`
	Force     bool `json:"force"`
}

type NFSShare struct {
	ID       int      `json:"id"`
	Path     string   `json:"path"`
	Comment  string   `json:"comment,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	ReadOnly bool     `json:"ro"`
	MapRoot  string   `json:"maproot,omitempty"`
	MapAll   string   `json:"mapall,omitempty"`
	Security []string `json:"security,omitempty"`
	Enabled  bool     `json:"enabled"`
	Networks []string `json:"networks,omitempty"`
}

type NFSShareCreateOptions struct {
	Path            string   `json:"path"`
	Comment         string   `json:"comment,omitempty"`
	Hosts           []string `json:"hosts,omitempty"`
	ReadOnly        bool     `json:"ro"`
	MapRootUser     *string  `json:"maproot_user,omitempty"`
	MapRootGroup    *string  `json:"maproot_group,omitempty"`
	MapAllUser      *string  `json:"mapall_user,omitempty"`
	MapAllGroup     *string  `json:"mapall_group,omitempty"`
	Security        []string `json:"security,omitempty"`
	Enabled         bool     `json:"enabled"`
	Networks        []string `json:"networks,omitempty"`
	ExposeSnapshots bool     `json:"expose_snapshots,omitempty"`
}

type ISCSITarget struct {
	ID     int                `json:"id"`
	Name   string             `json:"name"`
	Alias  string             `json:"alias,omitempty"`
	Mode   string             `json:"mode"`
	Groups []ISCSITargetGroup `json:"groups,omitempty"`
	Auth   *ISCSIAuth         `json:"auth,omitempty"`
}

type ISCSITargetCreateOptions struct {
	Name   string             `json:"name"`
	Alias  string             `json:"alias,omitempty"`
	Mode   string             `json:"mode"`
	Groups []ISCSITargetGroup `json:"groups,omitempty"`
}

type ISCSITargetGroup struct {
	Portal     int    `json:"portal"`
	Initiator  int    `json:"initiator,omitempty"`
	AuthMethod string `json:"authmethod,omitempty"`
	Auth       int    `json:"auth,omitempty"`
}

type ISCSIAuth struct {
	Tag        int    `json:"tag"`
	User       string `json:"user"`
	Secret     string `json:"secret"`
	PeerUser   string `json:"peeruser,omitempty"`
	PeerSecret string `json:"peersecret,omitempty"`
}

type ISCSIExtent struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"` // DISK or FILE
	Disk           string `json:"disk,omitempty"`
	Path           string `json:"path,omitempty"`
	FileSize       any    `json:"filesize,omitempty"` // Can be int64 or string in TrueNAS
	Serial         string `json:"serial,omitempty"`
	NAA            string `json:"naa,omitempty"`
	BlockSize      int    `json:"blocksize"`
	PBlockSize     bool   `json:"pblocksize"`
	AvailThreshold int    `json:"avail_threshold,omitempty"`
	Comment        string `json:"comment,omitempty"`
	InsecureTPC    bool   `json:"insecure_tpc"`
	XEN            bool   `json:"xen"`
	RPM            string `json:"rpm"`
	ReadOnly       bool   `json:"ro"`
	Enabled        bool   `json:"enabled"`
}

type ISCSIExtentCreateOptions struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Disk      string `json:"disk,omitempty"`
	BlockSize int    `json:"blocksize"`
	Enabled   bool   `json:"enabled"`
}

type ISCSITargetExtent struct {
	ID     int `json:"id"`
	Target int `json:"target"`
	Extent int `json:"extent"`
	LunID  int `json:"lunid"`
}

type ISCSITargetExtentCreateOptions struct {
	Target int `json:"target"`
	Extent int `json:"extent"`
	LunID  int `json:"lunid"`
}

type ISCSIPortal struct {
	ID                  int                 `json:"id"`
	Tag                 int                 `json:"tag"`
	Comment             string              `json:"comment,omitempty"`
	Listen              []ISCSIPortalListen `json:"listen"`
	DiscoveryAuthMethod string              `json:"discovery_authmethod,omitempty"`
	DiscoveryAuthGroup  int                 `json:"discovery_authgroup,omitempty"`
}

type ISCSIPortalListen struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type ISCSIInitiator struct {
	ID          int      `json:"id"`
	Initiators  []string `json:"initiators"`
	AuthNetwork []string `json:"auth_network,omitempty"`
	Comment     string   `json:"comment,omitempty"`
}

type Snapshot struct {
	ID         string         `json:"id"`
	Dataset    string         `json:"dataset"`
	Name       string         `json:"name"`
	CreateTime string         `json:"createtime"`
	Used       int64          `json:"used"`
	Referenced int64          `json:"referenced"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SnapshotCreateOptions struct {
	Dataset   string `json:"dataset"`
	Name      string `json:"name"`
	Recursive bool   `json:"recursive"`
}

type SnapshotDeleteOptions struct {
	Defer     bool `json:"defer"`
	Recursive bool `json:"recursive"`
}

type SnapshotClone struct {
	Snapshot   string `json:"snapshot"`
	DatasetDST string `json:"dataset_dst"`
}

type Pool struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	GUID      string `json:"guid"`
	Status    string `json:"status"`
	Healthy   bool   `json:"healthy"`
	Size      int64  `json:"size"`
	Allocated int64  `json:"allocated"`
	Free      int64  `json:"free"`
	Path      string `json:"path"`
	Autotrim  any    `json:"autotrim"` // Can be bool or object in TrueNAS
}

type ZFSResourceQueryOptions struct {
	Paths             []string `json:"paths"`
	Properties        []string `json:"properties,omitempty"`
	GetUserProperties bool     `json:"get_user_properties,omitempty"`
	GetSource         bool     `json:"get_source,omitempty"`
	NestResults       bool     `json:"nest_results,omitempty"`
	GetChildren       bool     `json:"get_children,omitempty"`
}

type ZFSResource struct {
	Name       string                 `json:"name"`
	Pool       string                 `json:"pool"`
	Type       string                 `json:"type"`
	Properties map[string]ZFSProperty `json:"properties"`
}

type ZFSProperty struct {
	Raw    string         `json:"raw"`
	Value  any            `json:"value"` // Can be int64, float64, string, bool, or null
	Source *ZFSPropSource `json:"source"`
}

type ZFSPropSource struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

func (c *Client) CreateDataset(ctx context.Context, options *DatasetCreateOptions) (*Dataset, error) {
	var result map[string]any
	err := c.Call(ctx, CREATE_DATASET, []any{options}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataset: %w", err)
	}
	dataset := &Dataset{
		Name: options.Name,
		Type: options.Type,
	}
	return dataset, nil
}

func (c *Client) GetDataset(ctx context.Context, path string) (*Dataset, error) {
	options := &DatasetQueryOptions{
		Extra: DatasetGetExtraOptions{
			Properties: []string{},
		},
	}

	var result map[string]any
	err := c.Call(ctx, GET_DATASET, []any{path, options}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to get dataset %s: %w", path, err)
	}

	// Extract basic fields from TrueNAS response
	dataset := &Dataset{
		Name: getString(result, "name"),
		Type: getString(result, "type"),
		Pool: getString(result, "pool"),
	}

	// Extract numeric fields - they come as objects with "parsed" field
	if refquota, ok := result["refquota"].(map[string]any); ok {
		if parsed, ok := refquota["parsed"].(float64); ok {
			dataset.RefQuota = int64(parsed)
		}
	}

	return dataset, nil
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (c *Client) DeleteDataset(ctx context.Context, path string) error {
	options := &DatasetDeleteOptions{
		Recursive: true,
		Force:     true,
	}

	err := c.Call(ctx, DELETE_DATASET, []any{path, options}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete dataset %s: %w", path, err)
	}
	return nil
}

func (c *Client) UpdateDataset(ctx context.Context, path string, updates *DatasetUpdateOptions) error {
	// TrueNAS returns a complex PoolDatasetEntry, but we just need to verify success
	var result any
	err := c.Call(ctx, UPDATE_DATASET, []any{path, updates}, &result)
	if err != nil {
		return fmt.Errorf("failed to update dataset %s: %w", path, err)
	}
	return nil
}

func (c *Client) CreateNFSShare(ctx context.Context, options *NFSShareCreateOptions) (*NFSShare, error) {
	var share NFSShare
	err := c.Call(ctx, CREATE_NFS_SHARE, []any{options}, &share)
	if err != nil {
		return nil, fmt.Errorf("failed to create NFS share: %w", err)
	}
	return &share, nil
}

func (c *Client) GetNFSShare(ctx context.Context, id int) (*NFSShare, error) {
	options := &QueryOptions{}

	var share NFSShare
	err := c.Call(ctx, GET_NFS_SHARE, []any{id, options}, &share)
	if err != nil {
		return nil, fmt.Errorf("failed to get NFS share %d: %w", id, err)
	}
	return &share, nil
}

func (c *Client) GetNFSShareByPath(ctx context.Context, path string) (*NFSShare, error) {
	filters := [][]any{
		{"path", "=", path},
	}
	options := &QueryOptions{}

	var shares []NFSShare
	err := c.Call(ctx, QUERY_NFS_SHARE, []any{filters, options}, &shares)
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	if len(shares) == 0 {
		return nil, fmt.Errorf("NFS share not found for path %s", path)
	}

	return &shares[0], nil
}

func (c *Client) DeleteNFSShare(ctx context.Context, id int) error {
	err := c.Call(ctx, DELETE_NFS_SHARE, []any{id}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete NFS share %d: %w", id, err)
	}
	return nil
}

func (c *Client) CreateISCSITarget(ctx context.Context, name, alias string) (*ISCSITarget, error) {
	params := &ISCSITargetCreateOptions{
		Name:  name,
		Alias: alias,
		Mode:  "ISCSI",
		Groups: []ISCSITargetGroup{
			{
				Portal: 1, // Use default portal group (ID 1)
			},
		},
	}

	var target ISCSITarget
	err := c.Call(ctx, CREATE_ISCSI_TARGET, []any{params}, &target)
	if err != nil {
		return nil, fmt.Errorf("failed to create iSCSI target: %w", err)
	}
	return &target, nil
}

func (c *Client) CreateISCSIExtent(ctx context.Context, name, disk string, blocksize int) (*ISCSIExtent, error) {
	params := &ISCSIExtentCreateOptions{
		Name:      name,
		Type:      "DISK",
		Disk:      disk,
		BlockSize: blocksize,
		Enabled:   true,
	}

	var extent ISCSIExtent
	err := c.Call(ctx, CREATE_ISCSI_EXTENT, []any{params}, &extent)
	if err != nil {
		return nil, fmt.Errorf("failed to create iSCSI extent: %w", err)
	}
	return &extent, nil
}

func (c *Client) CreateISCSITargetExtent(ctx context.Context, targetID, extentID, lunID int) (*ISCSITargetExtent, error) {
	params := &ISCSITargetExtentCreateOptions{
		Target: targetID,
		Extent: extentID,
		LunID:  lunID,
	}

	var targetExtent ISCSITargetExtent
	err := c.Call(ctx, CREATE_ISCSI_TARGET_EXTENT, []any{params}, &targetExtent)
	if err != nil {
		return nil, fmt.Errorf("failed to create target-extent association: %w", err)
	}
	return &targetExtent, nil
}

func (c *Client) DeleteISCSITarget(ctx context.Context, id int) error {
	var targetExtents []ISCSITargetExtent
	filters := [][]any{
		{"target", "=", id},
	}
	options := &QueryOptions{}

	err := c.Call(ctx, QUERY_ISCSI_TARGET_EXTENT, []any{filters, options}, &targetExtents)
	if err != nil {
		return fmt.Errorf("failed to query target-extent associations: %w", err)
	}

	for _, te := range targetExtents {
		if err := c.Call(ctx, DELETE_ISCSI_TARGET_EXTENT, []any{te.ID, false}, nil); err != nil {
			return fmt.Errorf("failed to delete target-extent %d: %w", te.ID, err)
		}
	}

	err = c.Call(ctx, DELETE_ISCSI_TARGET, []any{id, false, false}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete iSCSI target %d: %w", id, err)
	}
	return nil
}

func (c *Client) DeleteISCSIExtent(ctx context.Context, id int) error {
	err := c.Call(ctx, DELETE_ISCSI_EXTENT, []any{id, false, false}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete iSCSI extent %d: %w", id, err)
	}
	return nil
}

func (c *Client) CreateSnapshot(ctx context.Context, dataset, name string, recursive bool) (*Snapshot, error) {
	params := &SnapshotCreateOptions{
		Dataset:   dataset,
		Name:      name,
		Recursive: recursive,
	}

	var snapshot Snapshot
	err := c.Call(ctx, CREATE_SNAPSHOT, []any{params}, &snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}
	return &snapshot, nil
}

func (c *Client) DeleteSnapshot(ctx context.Context, name string) error {
	options := &SnapshotDeleteOptions{
		Defer: false,
	}

	err := c.Call(ctx, DELETE_SNAPSHOT, []any{name, options}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot %s: %w", name, err)
	}
	return nil
}

func (c *Client) CloneSnapshot(ctx context.Context, snapshot, destination string) (*Dataset, error) {
	params := SnapshotClone{
		Snapshot:   snapshot,
		DatasetDST: destination,
	}

	err := c.Call(ctx, CLONE_SNAPSHOT, []any{params}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to clone snapshot: %w", err)
	}

	return c.GetDataset(ctx, destination)
}

func (c *Client) ListSnapshots(ctx context.Context, dataset string) ([]Snapshot, error) {
	filters := [][]any{
		{"dataset", "=", dataset},
	}
	options := &QueryOptions{}

	var snapshots []Snapshot
	err := c.Call(ctx, QUERY_SNAPSHOT, []any{filters, options}, &snapshots)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}
	return snapshots, nil
}

func (c *Client) GetPool(ctx context.Context, name string) (*Pool, error) {
	var pools []Pool
	filters := [][]any{
		{"name", "=", name},
	}
	options := &QueryOptions{}

	err := c.Call(ctx, "pool.query", []any{filters, options}, &pools)
	if err != nil {
		return nil, fmt.Errorf("failed to query pool %s: %w", name, err)
	}
	if len(pools) == 0 {
		return nil, fmt.Errorf("pool '%s' not found in TrueNAS", name)
	}
	return &pools[0], nil
}

func (c *Client) ListPools(ctx context.Context) ([]Pool, error) {
	var pools []Pool
	filters := [][]any{} // Empty filters to get all pools
	options := &QueryOptions{}

	err := c.Call(ctx, "pool.query", []any{filters, options}, &pools)
	if err != nil {
		return nil, fmt.Errorf("failed to list pools: %w", err)
	}
	return pools, nil
}

func (c *Client) GetAvailableSpace(ctx context.Context, poolName string) (int64, error) {
	options := &ZFSResourceQueryOptions{
		Paths:      []string{poolName},
		Properties: []string{"available"},
		GetSource:  false,
	}

	var resources []ZFSResource
	err := c.Call(ctx, ZFS_RESOURCE_QUERY, []any{options}, &resources)
	if err != nil {
		return 0, fmt.Errorf("failed to query ZFS resource %s: %w", poolName, err)
	}

	if len(resources) == 0 {
		return 0, fmt.Errorf("ZFS resource '%s' not found", poolName)
	}

	resource := resources[0]
	availableProp, ok := resource.Properties["available"]
	if !ok {
		return 0, fmt.Errorf("'available' property not found for ZFS resource %s", poolName)
	}

	switch v := availableProp.Value.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("unexpected string value %v for 'available' property of ZFS resource %s", v, poolName)
		}
		return n, nil
	case nil:
		return 0, fmt.Errorf("'available' property is null for ZFS resource %s", poolName)
	default:
		return 0, fmt.Errorf("unexpected type %T for 'available' property of ZFS resource %s", v, poolName)
	}
}

func ExtractPoolFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
