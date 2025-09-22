package truenas

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"
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
		Method: "pool.create",
		Params: []any{opts},
	}

	return c.CallWithTimeout(ctx, req, nil, 5*time.Minute)
}

func (c *APIClient) GetPools(ctx context.Context, filters []any) (map[string]any, error) {
	klog.V(4).Info("Getting existing pools")

	var res map[string]any

	req := APIRequest{
		Method: "pool.query",
		Params: filters,
	}
	err := c.Call(ctx, req, &res)

	return res, err
}
