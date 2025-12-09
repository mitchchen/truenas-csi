package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/klog/v2"
)

const (
	DEFAULT_RECONNECT_INTERVAL  = 5 * time.Second
	DEFAULT_MAX_RECONNECT_DELAY = 5 * time.Minute
	DEFAULT_PING_INTERVAL       = 30 * time.Second
	DEFAULT_PONG_TIMEOUT        = 5 * time.Minute
	HANDSHAKE_TIMEOUT           = 30 * time.Second
	EXPONENTIAL_BACKOFF_MULTI   = 2
	READ_LOOP_SLEEP             = 100 * time.Millisecond
	WRITE_DEADLINE_ADD          = 10 * time.Second
	JSONRPC_VERSION             = "2.0"
)

type APIClient struct {
	url        url.URL
	apiKey     string
	conn       *websocket.Conn
	connMutex  sync.RWMutex
	requestID  atomic.Uint64
	pending    map[uint64]chan *Response
	pendingMux sync.RWMutex

	reconnectInterval  time.Duration
	maxReconnectDelay  time.Duration
	pingInterval       time.Duration
	pongTimeout        time.Duration
	insecureSkipVerify bool

	done chan struct{}
	wg   sync.WaitGroup
}

type Request struct {
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	JSONRPC string `json:"jsonrpc"`
}

type Response struct {
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("RPC error %d: %s (data: %s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// ClientConfig holds configuration for the TrueNAS client
type ClientConfig struct {
	URL                url.URL
	APIKey             string
	InsecureSkipVerify bool
	ReconnectInterval  time.Duration
	MaxReconnectDelay  time.Duration
	PingInterval       time.Duration
	PongTimeout        time.Duration
}

// NewClient creates a new TrueNAS WebSocket client
func NewClient(config *ClientConfig) (*APIClient, error) {
	if config.ReconnectInterval == 0 {
		config.ReconnectInterval = DEFAULT_RECONNECT_INTERVAL
	}
	if config.MaxReconnectDelay == 0 {
		config.MaxReconnectDelay = DEFAULT_MAX_RECONNECT_DELAY
	}
	if config.PingInterval == 0 {
		config.PingInterval = DEFAULT_PING_INTERVAL
	}
	if config.PongTimeout == 0 {
		config.PongTimeout = DEFAULT_PONG_TIMEOUT
	}

	client := &APIClient{
		url:                config.URL,
		apiKey:             config.APIKey,
		pending:            make(map[uint64]chan *Response),
		reconnectInterval:  config.ReconnectInterval,
		maxReconnectDelay:  config.MaxReconnectDelay,
		pingInterval:       config.PingInterval,
		pongTimeout:        config.PongTimeout,
		insecureSkipVerify: config.InsecureSkipVerify,
		done:               make(chan struct{}),
	}

	if err := client.connectWebSocket(config.InsecureSkipVerify); err != nil {
		close(client.done)
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	ctx := context.Background()
	client.wg.Add(2)
	go client.readLoop(ctx)
	go client.pingLoop(ctx)

	// Add random delay (0-3 seconds) to prevent multiple pods from authenticating simultaneously
	delay := time.Duration(rand.Intn(3000)) * time.Millisecond
	klog.V(3).Infof("Waiting %v before authentication to avoid rate limiting", delay)
	time.Sleep(delay)

	if err := client.authenticate(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}

	klog.V(2).Infof("Successfully authenticated with TrueNAS")

	return client, nil
}

// connectWebSocket establishes a WebSocket connection to TrueNAS (without authentication)
func (c *APIClient) connectWebSocket(insecureSkipVerify bool) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureSkipVerify,
		},
	}

	conn, _, err := dialer.Dial(c.url.String(), nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(c.pongTimeout))
		return nil
	})

	conn.SetReadDeadline(time.Now().Add(c.pongTimeout))

	c.connMutex.Lock()
	c.conn = conn
	c.connMutex.Unlock()

	klog.V(2).Infof("Connected to TrueNAS WebSocket API at %s", c.url.String())
	return nil
}

// authenticate performs authentication after connection
func (c *APIClient) authenticate() error {
	// Create a context with timeout for authentication
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var result bool
	// Authenticate with API key
	if c.apiKey == "" {
		return fmt.Errorf("no API key provided")
	}

	klog.V(3).Info("Authenticating with API key")
	err := c.Call(ctx, "auth.login_with_api_key", []string{c.apiKey}, &result)
	if err != nil {
		return fmt.Errorf("auth.login_with_api_key failed: %w", err)
	}

	klog.V(3).Infof("Authentication result: %v", result)
	if !result {
		return fmt.Errorf("authentication failed: credentials were rejected by TrueNAS")
	}

	return nil
}

// reconnect handles reconnection logic with exponential backoff
func (c *APIClient) reconnect(ctx context.Context) {
	delay := c.reconnectInterval

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-time.After(delay):
			klog.V(2).Info("Attempting to reconnect to TrueNAS...")

			dialer := websocket.Dialer{
				HandshakeTimeout: 10 * time.Second,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: c.insecureSkipVerify,
				},
			}

			conn, _, err := dialer.Dial(c.url.String(), nil)
			if err != nil {
				klog.Errorf("Reconnection failed: %v", err)

				// Exponential backoff
				delay *= 2
				if delay > c.maxReconnectDelay {
					delay = c.maxReconnectDelay
				}
				continue
			}

			conn.SetPongHandler(func(string) error {
				conn.SetReadDeadline(time.Now().Add(c.pongTimeout))
				return nil
			})

			conn.SetReadDeadline(time.Now().Add(c.pongTimeout))

			c.connMutex.Lock()
			c.conn = conn
			c.connMutex.Unlock()

			klog.V(2).Info("Reconnected to TrueNAS WebSocket API")

			c.wg.Add(1)
			go c.readLoop(context.Background())

			// Add random delay to avoid rate limiting on reconnection
			delay := time.Duration(rand.Intn(3000)) * time.Millisecond
			klog.V(3).Infof("Waiting %v before re-authentication to avoid rate limiting", delay)
			time.Sleep(delay)

			if err := c.authenticate(); err != nil {
				klog.Errorf("Re-authentication failed: %v", err)
				c.connMutex.Lock()
				c.conn.Close()
				c.conn = nil
				c.connMutex.Unlock()

				// Try again with backoff
				delay *= 2
				if delay > c.maxReconnectDelay {
					delay = c.maxReconnectDelay
				}
				continue
			}

			klog.Info("Successfully reconnected and authenticated with TrueNAS")
			return
		}
	}
}

// readLoop continuously reads messages from the WebSocket connection
func (c *APIClient) readLoop(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
			c.connMutex.RLock()
			conn := c.conn
			c.connMutex.RUnlock()

			if conn == nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			var resp Response
			err := conn.ReadJSON(&resp)
			if err != nil {
				klog.Errorf("Read error: %v", err)

				c.connMutex.Lock()
				c.conn.Close()
				c.conn = nil
				c.connMutex.Unlock()

				// Clear pending requests
				c.pendingMux.Lock()
				for id, ch := range c.pending {
					close(ch)
					delete(c.pending, id)
				}
				c.pendingMux.Unlock()

				// Trigger reconnection
				go c.reconnect(context.Background())
				return
			}

			conn.SetReadDeadline(time.Now().Add(c.pongTimeout))

			c.pendingMux.RLock()
			ch, ok := c.pending[resp.ID]
			c.pendingMux.RUnlock()

			if ok {
				ch <- &resp
				c.pendingMux.Lock()
				delete(c.pending, resp.ID)
				c.pendingMux.Unlock()
			}
		}
	}
}

// pingLoop sends periodic ping messages to keep the connection alive
func (c *APIClient) pingLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
			c.connMutex.Lock()
			if c.conn != nil {
				c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					klog.Errorf("Ping failed: %v", err)
				}
			}
			c.connMutex.Unlock()
		}
	}
}

// Call executes a JSON-RPC method and returns the result
func (c *APIClient) Call(ctx context.Context, method string, params any, result any) error {
	id := c.requestID.Add(1)

	req := Request{
		ID:      id,
		Method:  method,
		Params:  params,
		JSONRPC: JSONRPC_VERSION,
	}

	respCh := make(chan *Response, 1)
	c.pendingMux.Lock()
	c.pending[id] = respCh
	c.pendingMux.Unlock()

	c.connMutex.Lock()
	if c.conn == nil {
		c.connMutex.Unlock()
		return fmt.Errorf("no connection available")
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := c.conn.WriteJSON(req)
	c.connMutex.Unlock()

	if err != nil {
		c.pendingMux.Lock()
		delete(c.pending, id)
		c.pendingMux.Unlock()
		return fmt.Errorf("failed to send request: %w", err)
	}

	select {
	case <-ctx.Done():
		c.pendingMux.Lock()
		delete(c.pending, id)
		c.pendingMux.Unlock()
		return ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			return fmt.Errorf("connection closed while waiting for response")
		}

		if resp.Error != nil {
			return resp.Error
		}

		if result != nil && resp.Result != nil {
			return json.Unmarshal(resp.Result, result)
		}

		return nil
	}
}

// Close closes the WebSocket connection and cleans up resources
func (c *APIClient) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}

	c.connMutex.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.connMutex.Unlock()

	c.wg.Wait()
	return nil
}

// Ping checks if the connection is alive
func (c *APIClient) Ping(ctx context.Context) error {
	var result any
	return c.Call(ctx, "core.ping", nil, &result)
}

// GetSystemInfo retrieves system information
func (c *APIClient) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	err := c.Call(ctx, "system.info", nil, &info)
	return &info, err
}

// SystemInfo represents TrueNAS system information
type SystemInfo struct {
	Version              string    `json:"version"`
	BuildTime            any       `json:"buildtime"` // date-time object from TrueNAS
	Hostname             string    `json:"hostname"`
	PhysMem              int64     `json:"physmem"`
	Model                string    `json:"model"`
	Cores                int       `json:"cores"`
	PhysicalCores        int       `json:"physical_cores"`
	LoadAvg              []float64 `json:"loadavg"`
	Uptime               string    `json:"uptime"`
	UptimeSeconds        float64   `json:"uptime_seconds"`
	SystemSerial         *string   `json:"system_serial"`
	SystemProduct        *string   `json:"system_product"`
	SystemProductVersion *string   `json:"system_product_version"`
	License              any       `json:"license"`  // object or null
	BootTime             any       `json:"boottime"` // date-time object from TrueNAS
	DateTime             any       `json:"datetime"` // date-time object from TrueNAS
	Timezone             string    `json:"timezone"`
	SystemManufacturer   *string   `json:"system_manufacturer"`
	ECCMemory            bool      `json:"ecc_memory"`
}
