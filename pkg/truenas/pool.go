package truenas

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

func (c *APIClient) CreatePool(data PoolCreate) error {
	return nil
}
