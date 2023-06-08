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
type BeforeFunc func(context.Context, *http.Request) context.Context
type AfterFunc func(context.Context, http.ResponseWriter) context.Context
type ReqDecode func(ctx context.Context, r *http.Request, params json.RawMessage) (result any, err error)
type Endpoint func(ctx context.Context, request interface{}) (response interface{}, err error)
type EndpointMiddleware = func(Endpoint) Endpoint

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

type Server struct {
	methods    map[string]*ServerMethod
	before     []BeforeFunc
	after      []AfterFunc
	middleware []EndpointMiddleware
}

func (s *Server) makeErrorResponse(id any, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{ID: id, Version: Version, Error: &jsonRPCError{Code: code, Message: message}}
}

func (s *Server) handleMethod(method *ServerMethod, ctx context.Context, w http.ResponseWriter, r *http.Request, params json.RawMessage) (resp any, err error) {
	for _, before := range method.before {
		ctx = before(ctx, r)
	}
	request, err := method.reqDecode(ctx, r, params)
	if err != nil {
		return nil, err
	}
	response, err := middlewareChain(append(s.middleware, method.middleware...))(method.endpoint)(ctx, request)
	if err != nil {
		return nil, err
	}
	for _, after := range method.after {
		ctx = after(ctx, w)
	}
	return response, nil
}

func (s *Server) Register(path string, endpoint Endpoint, reqDecode ReqDecode) *ServerMethod {
	sm := &ServerMethod{}
	s.methods[path] = sm
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

func NewServer() *Server {
	return &Server{}
}

type ServerMethod struct {
	endpoint   Endpoint
	reqDecode  ReqDecode
	before     []BeforeFunc
	after      []AfterFunc
	middleware []EndpointMiddleware
}

func (sm *ServerMethod) SetAfter(after ...AfterFunc) *ServerMethod {
	sm.after = after
	return sm
}

func (sm *ServerMethod) SetBefore(before ...BeforeFunc) *ServerMethod {
	sm.before = before
	return sm
}
