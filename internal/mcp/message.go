package mcp

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0's error codes (https://www.jsonrpc.org/specification#error_object),
// which MCP uses as they are.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Message is one JSON-RPC message from the client, as Parse found it.
type Message struct {
	// ID is the request's id, a JSON string or number exactly as it came;
	// nil for a notification, which gets no answer.
	ID json.RawMessage
	// Method is what the request or notification asks for; "" for a
	// response to a request of the server's, which this server never
	// sends and so ignores.
	Method string
	// Params is the params object or array as it came, nil without one.
	Params json.RawMessage
}

// IsNotification reports whether m expects no answer.
func (m Message) IsNotification() bool { return m.ID == nil && m.Method != "" }

// IsResponse reports whether m answers a request rather than making one.
func (m Message) IsResponse() bool { return m.Method == "" }

// Parse reads one line from the client. A line that is not a JSON-RPC 2.0
// request, notification or response comes back as the error to answer it
// with, and m.ID as the request's id when that could be read, else nil
// (the answer then carries a null id). Batches, which MCP has not had
// since 2025-06-18, are refused as invalid requests.
func Parse(line []byte) (m Message, perr *Error) {
	line = bytes.TrimSpace(line)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		var value json.RawMessage
		if json.Unmarshal(line, &value) == nil {
			// Valid JSON, but not an object: a batch or a bare value.
			return Message{}, &Error{CodeInvalidRequest, "invalid request: a message is one JSON object (batches are not supported)"}
		}
		return Message{}, &Error{CodeParseError, "parse error: not JSON"}
	}
	if id, ok := fields["id"]; ok {
		id = bytes.TrimSpace(id)
		if !validID(id) {
			return Message{}, &Error{CodeInvalidRequest, "invalid request: an id is a string or a number"}
		}
		m.ID = id
	}
	if v, ok := fields["jsonrpc"]; !ok || string(bytes.TrimSpace(v)) != `"2.0"` {
		return Message{ID: m.ID}, &Error{CodeInvalidRequest, `invalid request: "jsonrpc" must be "2.0"`}
	}
	method, hasMethod := fields["method"]
	if !hasMethod {
		_, result := fields["result"]
		_, rpcErr := fields["error"]
		if result || rpcErr {
			return m, nil // a response: nothing to do
		}
		return Message{ID: m.ID}, &Error{CodeInvalidRequest, `invalid request: no "method"`}
	}
	if err := json.Unmarshal(method, &m.Method); err != nil || m.Method == "" {
		return Message{ID: m.ID}, &Error{CodeInvalidRequest, `invalid request: "method" must be a non-empty string`}
	}
	if p, ok := fields["params"]; ok {
		p = bytes.TrimSpace(p)
		if len(p) == 0 || (p[0] != '{' && p[0] != '[') {
			return Message{ID: m.ID}, &Error{CodeInvalidRequest, `invalid request: "params" must be an object`}
		}
		m.Params = p
	}
	return m, nil
}

// validID reports whether raw, a JSON value, is an id MCP allows: a string
// or a number. MCP forbids null, which JSON-RPC merely discourages.
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '"':
		var s string
		return json.Unmarshal(raw, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		return json.Unmarshal(raw, &n) == nil
	}
	return false
}

// response is a JSON-RPC response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// encode writes the response as one line: encoding/json never writes a
// raw newline inside a value, so a message cannot span lines.
func (r response) encode() []byte {
	r.JSONRPC = "2.0"
	if r.ID == nil {
		r.ID = json.RawMessage("null")
	}
	if r.Result == nil && r.Error == nil {
		r.Result = struct{}{}
	}
	data, err := json.Marshal(r)
	if err != nil {
		data, _ = json.Marshal(response{JSONRPC: "2.0", ID: r.ID, Error: &Error{CodeInternalError, "internal error: the answer could not be encoded"}})
	}
	return append(data, '\n')
}
