package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/klog/v2"
)

type APIMethod string

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
	JsonRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Method  APIMethod `json:"method"`
	Params  []any     `json:"params"`
}

type APIResponse struct {
	JsonRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *APIError
}

type APIError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *APIErrorData `json:"data"`
}

type APIErrorData struct {
	Error   int     `json:"error"`
	Message *string `json:"message"`
	Reason  string  `json:"reason"`
	Trace   struct {
		Class     string           `json:"class"`
		Frames    []map[string]any `json:"frames"`
		Formatted string           `json:"formatted"`
		Repr      string           `json:"repr"`
	} `json:"trace"`
	Extra       []map[string]any `json:"extra"`
	PyException string           `json:"py_exception"`
}

type PendingRequest struct {
	request  APIRequest
	response chan *APIResponse
	context  context.Context
	timeout  time.Duration
}

type APIClient struct {
	endpoint       url.URL
	username       string
	password       string
	defaultTimeout time.Duration

	conn          *websocket.Conn
	connected     bool
	authenticated bool
	connMu        sync.RWMutex

	pendingMu sync.RWMutex
	pending   map[string]*PendingRequest
	requests  chan *PendingRequest

	ctx    context.Context
	cancel context.CancelFunc
	done   chan int
	wg     sync.WaitGroup
}

func NewAPIClient(endpoint url.URL, username string, password string) *APIClient {
	ctx, cancel := context.WithCancel(context.Background())

	c := &APIClient{
		endpoint:       endpoint,
		username:       username,
		password:       password,
		connected:      false,
		authenticated:  false,
		defaultTimeout: 30 * time.Second,
		connMu:         sync.RWMutex{},
		pending:        map[string]*PendingRequest{},
		requests:       make(chan *PendingRequest, 100),
		wg:             sync.WaitGroup{},
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan int),
	}

	c.wg.Add(2)
	go c.connectionManager()
	go c.requestHandler()

	return c
}

func (c *APIClient) connectionManager() {
	defer c.wg.Done()
	defer close(c.done)

	reconnectDelay := time.Second
	maxReconnectDelay := 30 * time.Second

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		if err := c.connect(); err != nil {
			klog.Errorf("Failed to connect to TrueNAS: %v", err)
			c.setConnectionState(false, false)

			select {
			case <-time.After(reconnectDelay):
				reconnectDelay = min(reconnectDelay*2, maxReconnectDelay)
			case <-c.ctx.Done():
				return
			}
			continue
		}

		reconnectDelay = time.Second
		c.setConnectionState(true, false)

		if err := c.authenticate(); err != nil {
			klog.Errorf("Authenication failed: %v", err)
			c.closeConnection()
			continue
		}

		c.setConnectionState(true, true)

		klog.V(2).Info("Succesfully connected and authenticated to TrueNAS")

		c.wg.Add(1)
		go c.reader()

		select {
		case <-c.done:
			return
		case <-c.ctx.Done():
			return
		}

	}
}

func (c *APIClient) reader() {
	defer c.wg.Done()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return
	}

	for {
		var res APIResponse
		if err := conn.ReadJSON(&res); err != nil {
			klog.Errorf("Websocket read error: %v", err)
			c.closeConnection()

			c.failPendingRequests(fmt.Errorf("connection lost: %w", err))
			return
		}

		c.handleResponse(&res)
	}
}

func (c *APIClient) handleResponse(res *APIResponse) {
	c.pendingMu.RLock()
	pending, ok := c.pending[res.ID]
	c.pendingMu.RUnlock()

	if !ok {
		klog.Warningf("Recieved response for unknown request ID: %s", res.ID)
		return
	}

	c.pendingMu.Lock()
	delete(c.pending, res.ID)
	c.pendingMu.Unlock()

	select {
	case pending.response <- res:
	case <-pending.context.Done():
	default:
		klog.Warningf("Response channel full for request %s", res.ID)

	}
}

func (c *APIClient) failPendingRequests(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]*PendingRequest)
	c.pendingMu.Unlock()

	for _, req := range pending {
		select {
		case req.response <- &APIResponse{
			ID:    req.request.ID,
			Error: &APIError{Code: -1, Message: err.Error()},
		}:
		case <-req.context.Done():
		}
	}
}

func (c *APIClient) processRequest(req *PendingRequest) {
	c.connMu.RLock()
	connected := c.connected && c.authenticated
	conn := c.conn
	c.connMu.RUnlock()

	if !connected || conn == nil {
		select {
		case req.response <- &APIResponse{
			ID:    req.request.ID,
			Error: &APIError{Code: -1, Message: "not connected to TrueNAS"},
		}:
		case <-req.context.Done():
		}
		return
	}

	c.pendingMu.Lock()
	c.pending[req.request.ID] = req
	c.pendingMu.Unlock()

	timer := time.NewTimer(req.timeout)
	defer timer.Stop()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(req.request); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, req.request.ID)
		c.pendingMu.Unlock()

		select {
		case req.response <- &APIResponse{
			ID:    req.request.ID,
			Error: &APIError{Code: -1, Message: fmt.Sprintf("write error: %v", err)},
		}:
		case <-req.context.Done():
		}
		return
	}

	select {
	case <-timer.C:
		c.pendingMu.Lock()
		delete(c.pending, req.request.ID)
		c.pendingMu.Unlock()

		select {
		case req.response <- &APIResponse{
			ID:    req.request.ID,
			Error: &APIError{Code: -1, Message: "request timeout"},
		}:
		case <-req.context.Done():
		}
	case <-req.context.Done():
		c.pendingMu.Lock()
		delete(c.pending, req.request.ID)
		c.pendingMu.Unlock()
	}
}

func (c *APIClient) requestHandler() {
	defer c.wg.Done()

	for {
		select {
		case req := <-c.requests:
			c.processRequest(req)
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *APIClient) setConnectionState(connected, authenticated bool) {
	c.connMu.Lock()
	c.connected = connected
	c.authenticated = authenticated
	c.connMu.Unlock()
}

func (c *APIClient) closeConnection() {
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	c.setConnectionState(false, false)
}

func (c *APIClient) authenticate() error {
	req := APIRequest{
		Method: "auth.login",
		Params: []any{c.username, c.password},
		ID:     "auth",
	}

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return fmt.Errorf("no connection available")
	}

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(req); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	var res APIResponse
	if err := conn.ReadJSON(&res); err != nil {
		return err
	}

	if res.Error != nil {
		return fmt.Errorf("error: %v", res.Error.Message)
	}

	return nil
}

func (c *APIClient) connect() error {
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.DialContext(c.ctx, c.endpoint.String(), nil)
	if err != nil {
		return err
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	return nil
}

func (c *APIClient) CallWithTimeout(ctx context.Context, req APIRequest, res any, timeout time.Duration) error {
	pendingReq := &PendingRequest{
		request:  req,
		response: make(chan *APIResponse, 1),
		context:  ctx,
		timeout:  timeout,
	}

	select {
	case c.requests <- pendingReq:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return fmt.Errorf("client is shutting down")
	}

	select {
	case response := <-pendingReq.response:
		if response.Error != nil {
			return fmt.Errorf("error: %v", response.Error.Message)
		}
		if res != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, res); err != nil {
				return fmt.Errorf("failed to unmarshal result: %w", err)
			}
		}
		return nil

	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return fmt.Errorf("client is shutting down")
	}
}

func (c *APIClient) Call(ctx context.Context, req APIRequest, res any) error {
	return c.CallWithTimeout(ctx, req, res, c.defaultTimeout)
}

func (c *APIClient) Close() {
	c.cancel()
	c.closeConnection()
	c.wg.Wait()
}
