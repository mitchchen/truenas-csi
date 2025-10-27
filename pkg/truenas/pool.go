package truenas

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"
)

const (
	POOL_CREATE APIMethod = "pool.create"
	POOL_QUERY  APIMethod = "pool.query"
)

const (
	POOL_CREATE_TIMEOUT = 5 * time.Minute
	POOL_REMOVE_TIMEOUT = 5 * time.Minute
)

// TODO:Change when it is eventually changed
type PoolCreate struct {
	Name                 string  `json:"name"`
	Encryption           *bool   `json:"encryption"`
	DedupTableQuota      *string `json:"dedup_table_quota"`
	DedupTableQuotaValue *int    `json:"dedup_table_quota_value"`
	Deduplication        *string `json:"deduplication"`
	Checksum             *string `json:"Checksum"`
	EncryptionOptions    *struct {
		GenerateKey bool `json:"generate_key"`
		Pbkdf2iters int
		Algorithm   string
		Passphrase  *string
		Key         *string
	} `json:"encryption_options"`
	Topology struct{} `json:"topology"`
}

func (c *APIClient) CreatePool(ctx context.Context, opts PoolCreate) error {
	klog.V(2).Infof("Creating ZFS pool: %s", opts.Name)

	if opts.Name == "" {
		return fmt.Errorf("pool name is required")
	}

	req := APIRequest{
		Method: POOL_CREATE,
		Params: []any{opts},
	}

	return c.CallWithTimeout(ctx, req, nil, POOL_CREATE_TIMEOUT)
}

func (c *APIClient) GetPools(ctx context.Context, filters []any) (map[string]any, error) {
	klog.V(2).Info("Getting existing pools")

	var res map[string]any

	req := APIRequest{
		Method: POOL_QUERY,
		Params: filters,
	}
	err := c.Call(ctx, req, &res)

	return res, err
}

func (c *APIClient) RemovePool(ctx context.Context, poolID int, options map[string]any) error {
	klog.V(2).Infof("Removing pool with ID: %d", poolID)

	if options == nil {
		options = make(map[string]any)
	}

	req := APIRequest{
		Method: "pool.remove",
		Params: []any{poolID, options},
	}

	if err := c.CallWithTimeout(ctx, req, nil, POOL_REMOVE_TIMEOUT); err != nil {
		return fmt.Errorf("failed to remove pool %d: %w", poolID, err)
	}

	klog.V(2).Infof("Successfully removed pool %d", poolID)
	return nil
}

func (c *APIClient) GetPoolByName(ctx context.Context, poolName string) (int, map[string]any, error) {
	klog.V(4).Infof("Getting pool by name: %s", poolName)

	var result []map[string]any

	filters := []any{
		[]any{"name", "=", poolName},
	}

	req := APIRequest{
		Method: "pool.query",
		Params: []any{filters},
	}

	if err := c.Call(ctx, req, &result); err != nil {
		return 0, nil, fmt.Errorf("failed to query pool: %w", err)
	}

	if len(result) == 0 {
		return 0, nil, fmt.Errorf("pool not found: %s", poolName)
	}

	poolData := result[0]
	poolID, ok := poolData["id"].(int)
	if !ok {
		return 0, nil, fmt.Errorf("invalid pool ID format")
	}

	return poolID, poolData, nil
}

func (c *APIClient) RemovePoolByName(ctx context.Context, poolName string, cascade bool) error {
	klog.V(2).Infof("Removing pool by name: %s", poolName)

	poolID, _, err := c.GetPoolByName(ctx, poolName)
	if err != nil {
		return err
	}

	options := map[string]any{
		"cascade": cascade,
		"destroy": true,
	}

	return c.RemovePool(ctx, poolID, options)
}

func (c *APIClient) ExportPool(ctx context.Context, poolID int, force bool) error {
	klog.V(2).Infof("Exporting pool: %d", poolID)

	options := map[string]any{
		"cascade": false,
		"destroy": false,
	}

	if force {
		options["force"] = true
	}

	if err := c.RemovePool(ctx, poolID, options); err != nil {
		return fmt.Errorf("failed to export pool %d: %w", poolID, err)
	}

	klog.V(2).Infof("Successfully exported pool %d", poolID)
	return nil
}

func (c *APIClient) ExportPoolByName(ctx context.Context, poolName string, force bool) error {
	poolID, _, err := c.GetPoolByName(ctx, poolName)
	if err != nil {
		return err
	}

	return c.ExportPool(ctx, poolID, force)
}
