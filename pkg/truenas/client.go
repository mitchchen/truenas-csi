package truenas

const (
	INVALID_REQUEST       = -32600
	METHOD_NOT_FOUND      = -32601
	INVALID_PARAMS        = -32602
	INTERNAL_ERROR        = -32603
	PARSE_ERROR           = -32700
	CONCURRENT_CALL_ERROR = -32000
	METHOD_CALL_ERROR     = -32001
)

type APIRequest struct {
	JsonRPC string `json:"jsonrpc"`
	Id      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type APIResponse struct {
	JsonRPC string         `json:"jsonrpc"`
	Id      string         `json:"id"`
	Result  map[string]any `json:"result"`
	Error   *APIError
}

type APIError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}
