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

const POOL_CREATE_TIMEOUT = 5 * time.Minute

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
