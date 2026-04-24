package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidReq     = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	// Application-defined (JSON-RPC reserves -32000..-32099 for server errors).
	ErrCodeUnauthorized = -32000
	ErrCodeNotFound     = -32001
	ErrCodeConflict     = -32002
	ErrCodeDispatch     = -32003
	ErrCodeUnavailable  = -32004
)

// Request is a parsed JSON-RPC 2.0 request envelope.
type Request struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// Response is a JSON-RPC 2.0 response envelope.
type Response struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MethodError is returned by a command handler to propagate an application-
// level error with a specific JSON-RPC error code.
type MethodError struct {
	Code    int
	Message string
}

func (e *MethodError) Error() string { return e.Message }

// ParseRequest decodes a JSON-RPC envelope from body bytes. On failure returns
// a response populated with a parse-error object (id=null per spec).
func ParseRequest(body []byte) (*Request, *Response) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errorResponse(nil, ErrCodeInvalidReq, "empty request")
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errorResponse(nil, ErrCodeParseError, "invalid JSON")
	}
	if req.Jsonrpc != "2.0" {
		return nil, errorResponse(req.ID, ErrCodeInvalidReq, "jsonrpc must be \"2.0\"")
	}
	if strings.TrimSpace(req.Method) == "" {
		return nil, errorResponse(req.ID, ErrCodeInvalidReq, "method is required")
	}
	return &req, nil
}

// Dispatch runs a method against the registry and returns a response envelope.
// Auth is the caller's responsibility; Dispatch assumes actor is already
// authorized for admin access. Handlers can still reject with MethodError if a
// specific target is unauthorized.
func Dispatch(req *Request, env *Env, actor Actor) *Response {
	cmd, ok := lookupCommand(req.Method)
	if !ok {
		msg := fmt.Sprintf("method %q not found", req.Method)
		auditRPC(env, actor, req.Method, "error", msg)
		return errorResponse(req.ID, ErrCodeMethodNotFound, msg)
	}

	params, err := cmd.ParseParams(req.Params)
	if err != nil {
		auditRPC(env, actor, req.Method, "error", err.Error())
		return errorResponse(req.ID, ErrCodeInvalidParams, err.Error())
	}

	auditRPC(env, actor, req.Method, "request", params)

	result, err := cmd.Handler(env, actor, params)
	if err != nil {
		var me *MethodError
		code := ErrCodeInternal
		if errors.As(err, &me) {
			code = me.Code
		}
		auditRPC(env, actor, req.Method, "error", err.Error())
		return errorResponse(req.ID, code, err.Error())
	}

	auditRPC(env, actor, req.Method, "result", result)
	return &Response{Jsonrpc: "2.0", Result: result, ID: req.ID}
}

func errorResponse(id json.RawMessage, code int, message string) *Response {
	return &Response{Jsonrpc: "2.0", Error: &RPCError{Code: code, Message: message}, ID: id}
}

// auditRPC emits a single structured audit record for one RPC leg. direction
// is one of "request" / "result" / "error"; payload is JSON-marshaled and
// flattened into one audit line.
func auditRPC(env *Env, actor Actor, method, direction string, payload any) {
	if env == nil || env.Auditf == nil {
		return
	}
	bytes, _ := json.Marshal(payload)
	env.Auditf("admin-rcon %s actor=%q method=%s %s=%s",
		direction, actor.ID, method, direction, sanitizeAuditText(string(bytes)))
}

const auditTextMax = 512

func sanitizeAuditText(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "<empty>"
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > auditTextMax {
		text = text[:auditTextMax-3] + "..."
	}
	return text
}
