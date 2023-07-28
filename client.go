package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

type ClientBeforeFunc func(context.Context, *http.Request) context.Context
type ClientAfterFunc func(context.Context, *http.Response, json.RawMessage) context.Context

type clientOptions struct {
	ctx        context.Context
	before     []ClientBeforeFunc
	after      []ClientAfterFunc
	httpClient *http.Client
}
type ClientOption func(*clientOptions)

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(o *clientOptions) {
		o.httpClient = httpClient
	}
}

func WithContext(ctx context.Context) ClientOption {
	return func(o *clientOptions) {
		o.ctx = ctx
	}
}

func BeforeRequest(before ...ClientBeforeFunc) ClientOption {
	return func(o *clientOptions) {
		o.before = append(o.before, before...)
	}
}

func AfterRequest(after ...ClientAfterFunc) ClientOption {
	return func(o *clientOptions) {
		o.after = append(o.after, after...)
	}
}

type requester interface {
	MakeRequest() (string, any)
	MakeResult(data []byte) (any, error)
}

type requesterWithBefore interface {
	Before() []ClientBeforeFunc
}

type requesterWithAfter interface {
	After() []ClientAfterFunc
}

type requesterWithContext interface {
	Context() context.Context
}

type clientReq struct {
	ID      uint64 `json:"id"`
	Version string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type clientResp struct {
	ID      uint64          `json:"id"`
	Version string          `json:"jsonrpc"`
	Error   *clientError    `json:"error"`
	Result  json.RawMessage `json:"result"`
}

type clientError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type BatchResult struct {
	results []any
}

func (r *BatchResult) At(i int) any {
	return r.results[i]
}
func (r *BatchResult) Len() int {
	return len(r.results)
}

type Client struct {
	target      string
	incrementID uint64
	opts        *clientOptions
}

func (c *Client) autoIncrementID() uint64 {
	return atomic.AddUint64(&c.incrementID, 1)
}

func (c *Client) doRequests(requests []requester) (data []byte, idsIndex map[uint64]int, resp *http.Response, err error) {
	c.incrementID = 0
	req, err := http.NewRequest("POST", c.target, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	idsIndex = make(map[uint64]int, len(requests))
	rpcRequests := make([]clientReq, len(requests))
	for _, beforeFunc := range c.opts.before {
		req = req.WithContext(beforeFunc(req.Context(), req))
	}
	for i, request := range requests {
		if v, ok := request.(requesterWithContext); ok && v.Context() != nil {
			req = req.WithContext(v.Context())
		}
		if r, ok := request.(requesterWithBefore); ok {
			for _, beforeFunc := range r.Before() {
				req = req.WithContext(beforeFunc(req.Context(), req))
			}
		}
		methodName, params := request.MakeRequest()
		r := clientReq{ID: c.autoIncrementID(), Version: "2.0", Method: methodName, Params: params}
		idsIndex[r.ID] = i
		rpcRequests[i] = r
	}

	reqBuf := bytes.NewBuffer(nil)
	if err := json.NewEncoder(reqBuf).Encode(rpcRequests); err != nil {
		return nil, nil, nil, err
	}
	req.Body = io.NopCloser(reqBuf)
	resp, err = c.opts.httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.StatusCode != 200 {
		return nil, nil, nil, errors.New(resp.Status)
	}
	var wb = make([]byte, 0, 10485760)
	buf := bytes.NewBuffer(wb)
	written, err := io.Copy(buf, resp.Body)
	if err != nil {
		return nil, nil, nil, err
	}
	data = wb[:written]
	return
}

func (c *Client) RawExecute(requests ...requester) ([]byte, map[uint64]int, *http.Response, error) {
	return c.doRequests(requests)
}

func (c *Client) Execute(requests ...requester) (*BatchResult, error) {
	data, idsIndex, resp, err := c.doRequests(requests)
	if err != nil {
		return nil, err
	}
	responses := make([]clientResp, len(requests))
	if err := json.Unmarshal(data, &responses); err != nil {
		return nil, err
	}
	batchResult := &BatchResult{results: make([]any, len(requests))}
	for _, response := range responses {
		for _, afterFunc := range c.opts.after {
			afterFunc(resp.Request.Context(), resp, response.Result)
		}
		i := idsIndex[response.ID]
		request := requests[i]
		if v, ok := request.(requesterWithAfter); ok {
			for _, afterFunc := range v.After() {
				afterFunc(resp.Request.Context(), resp, response.Result)
			}
		}
		result, err := request.MakeResult(response.Result)
		if err != nil {
			return nil, err
		}
		batchResult.results[i] = result
	}
	return batchResult, nil
}

func NewClient(target string, opts ...ClientOption) *Client {
	c := &Client{target: target, opts: &clientOptions{}}
	for _, opt := range opts {
		opt(c.opts)
	}
	if c.opts.httpClient == nil {
		c.opts.httpClient = http.DefaultClient
	}
	return c
}
