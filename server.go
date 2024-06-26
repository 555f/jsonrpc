package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

const Version = "2.0"

const jsonRPCParseError int = -32700
const jsonRPCInvalidRequestError int = -32600
const jsonRPCMethodNotFoundError int = -32601
const jsonRPCInvalidParamsError int = -32602
const jsonRPCInternalError int = -32603

type ErrorEncoder func(ctx context.Context, err error, w http.ResponseWriter)
type BeforeFunc func(ctx context.Context, r *http.Request) (newCtx context.Context, err error)
type AfterFunc func(ctx context.Context, rw http.ResponseWriter) (newCtx context.Context)
type ReqDecode func(ctx context.Context, r *http.Request, params json.RawMessage) (result any, err error)
type Endpoint func(ctx context.Context, request interface{}) (response interface{}, err error)
type EndpointMiddlewareFunc = func(Endpoint) Endpoint

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCRequest struct {
	ID      any             `json:"id"`
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	ID      any             `json:"id"`
	Version string          `json:"jsonrpc"`
	Error   *jsonRPCError   `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type jsonRPCRequestData struct {
	requests []jsonRPCRequest
	isBatch  bool
}

func (r *jsonRPCRequestData) UnmarshalJSON(b []byte) error {
	if bytes.HasPrefix(b, []byte("[")) {
		r.isBatch = true
		return json.Unmarshal(b, &r.requests)
	}
	var req jsonRPCRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return err
	}
	r.requests = append(r.requests, req)
	return nil
}

type Option func(*Options)

func Before(before ...BeforeFunc) Option {
	return func(o *Options) {
		o.before = append(o.before, before...)
	}
}

func After(after ...AfterFunc) Option {
	return func(o *Options) {
		o.after = append(o.after, after...)
	}
}

func EndpointMiddleware(middleware ...EndpointMiddlewareFunc) Option {
	return func(o *Options) {
		o.middleware = append(o.middleware, middleware...)
	}
}

type Options struct {
	before     []BeforeFunc
	after      []AfterFunc
	middleware []EndpointMiddlewareFunc
}

type ServerMethod struct {
	endpoint  Endpoint
	reqDecode ReqDecode
	opts      *Options
}

type Server struct {
	methods map[string]*ServerMethod
	opts    *Options
}

func (s *Server) makeErrorResponse(id any, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{ID: id, Version: Version, Error: &jsonRPCError{Code: code, Message: message}}
}

func (s *Server) handleMethod(method *ServerMethod, ctx context.Context, w http.ResponseWriter, r *http.Request, params json.RawMessage) (resp any, err error) {
	for _, before := range method.opts.before {
		ctx, err = before(ctx, r)
		if err != nil {
			return
		}
	}
	request, err := method.reqDecode(ctx, r, params)
	if err != nil {
		return nil, err
	}
	response, err := middlewareChain(method.opts.middleware)(method.endpoint)(ctx, request)
	if err != nil {
		return nil, err
	}
	for _, after := range method.opts.after {
		ctx = after(ctx, w)
	}
	return response, nil
}

func (s *Server) Register(method string, endpoint Endpoint, reqDecode ReqDecode, opts ...Option) *ServerMethod {
	o := &Options{
		before:     s.opts.before,
		after:      s.opts.after,
		middleware: s.opts.middleware,
	}
	for _, opt := range opts {
		opt(o)
	}
	sm := &ServerMethod{opts: o, endpoint: endpoint, reqDecode: reqDecode}
	s.methods[method] = sm
	return sm
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var requestData jsonRPCRequestData
	var responses []jsonRPCResponse
	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		responses = append(responses, s.makeErrorResponse(nil, jsonRPCParseError, err.Error()))
	} else {
		for _, req := range requestData.requests {
			method, ok := s.methods[req.Method]
			if !ok {
				responses = append(responses, s.makeErrorResponse(req.ID, jsonRPCMethodNotFoundError, "method "+req.Method+" not found"))
				continue
			}
			resp, err := s.handleMethod(method, ctx, w, r, req.Params)
			if err != nil {
				responses = append(responses, s.makeErrorResponse(req.ID, jsonRPCInternalError, err.Error()))
				continue
			}
			result, err := json.Marshal(resp)
			if err != nil {
				responses = append(responses, s.makeErrorResponse(req.ID, jsonRPCInternalError, err.Error()))
				continue
			}
			responses = append(responses, jsonRPCResponse{ID: req.ID, Version: "2.0", Result: result})
		}
	}
	var data any
	if requestData.isBatch {
		data = responses
	} else {
		data = responses[0]
	}
	_ = json.NewEncoder(w).Encode(data)
}

func NewServer(opts ...Option) *Server {
	o := &Options{}
	for _, opt := range opts {
		opt(o)
	}
	return &Server{methods: make(map[string]*ServerMethod, 128), opts: o}
}
