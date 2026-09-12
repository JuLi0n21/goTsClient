package rpc

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sync"

	coderWS "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type RouteInvoker func(ctx context.Context, rawParams []any) (any, error)
type StreamInvoker func(ctx context.Context, rawParams []any, sendChunk func(data any) error) error

type Stream[T any] struct {
	ctx  context.Context
	send func(item T) error
}

func (s *Stream[T]) Send(item T) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
		return s.send(item)
	}
}

func (s *Stream[T]) Context() context.Context {
	return s.ctx
}

type wsConn struct {
	conn *coderWS.Conn
	mu   sync.Mutex
}

func (c *wsConn) SendJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsjson.Write(context.Background(), c.conn, v)
}

type wsRPCRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params []any  `json:"params"`
	Token  string `json:"token"`
	Cancel bool   `json:"cancel"`
}

type streamTracker struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (s *streamTracker) add(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()
}

func (s *streamTracker) cancel(id string) {
	s.mu.Lock()
	if c, ok := s.cancels[id]; ok {
		c()
		delete(s.cancels, id)
	}
	s.mu.Unlock()
}

func (s *streamTracker) cancelAll() {
	s.mu.Lock()
	for _, c := range s.cancels {
		c()
	}
	s.cancels = make(map[string]context.CancelFunc)
	s.mu.Unlock()
}

type Handler struct {
	apiStruct            any
	isTokenValid         func(token string, method string, params []any) bool
	compressionEnabled   bool
	compressionThreshold int
	routesMu             sync.RWMutex
	routes               map[string]RouteInvoker
	streamRoutesMu       sync.RWMutex
	streamRoutes         map[string]StreamInvoker
	connsMu              sync.RWMutex
	conns                map[*wsConn]struct{}
}

func NewHandler() *Handler {
	return &Handler{
		compressionEnabled:   true,
		compressionThreshold: 512,
		routes:               make(map[string]RouteInvoker),
		streamRoutes:         make(map[string]StreamInvoker),
		conns:                make(map[*wsConn]struct{}),
	}
}

func (h *Handler) SetCompression(enabled bool, threshold int) *Handler {
	h.compressionEnabled = enabled
	h.compressionThreshold = threshold
	return h
}

func (h *Handler) SetTokenValidator(fn func(token string, method string, params []any) bool) *Handler {
	h.isTokenValid = fn
	return h
}

func (h *Handler) SetAPIStruct(apiStruct any) *Handler {
	h.apiStruct = apiStruct
	return h
}

func decodeParam(raw any, target any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.NewDecoder(bytes.NewReader(b)).Decode(target)
}

func (h *Handler) Register[Req, Resp any](name string, fn func(ctx context.Context, req Req) (Resp, error)) *Handler {
	h.routesMu.Lock()
	defer h.routesMu.Unlock()
	h.routes[name] = func(ctx context.Context, rawParams []any) (any, error) {
		if len(rawParams) != 1 {
			return nil, fmt.Errorf("invalid param count: expected 1, got %d", len(rawParams))
		}
		var req Req
		if err := decodeParam(rawParams[0], &req); err != nil {
			return nil, fmt.Errorf("param 0 unmarshal error: %w", err)
		}
		return fn(ctx, req)
	}
	return h
}

func (h *Handler) RegisterNoArg[Resp any](name string, fn func(ctx context.Context) (Resp, error)) *Handler {
	h.routesMu.Lock()
	defer h.routesMu.Unlock()
	h.routes[name] = func(ctx context.Context, rawParams []any) (any, error) {
		if len(rawParams) != 0 {
			return nil, fmt.Errorf("invalid param count: expected 0, got %d", len(rawParams))
		}
		return fn(ctx)
	}
	return h
}

func (h *Handler) RegisterStream[Req, Item any](name string, fn func(ctx context.Context, req Req, stream *Stream[Item]) error) *Handler {
	h.streamRoutesMu.Lock()
	defer h.streamRoutesMu.Unlock()
	h.streamRoutes[name] = func(ctx context.Context, rawParams []any, sendChunk func(data any) error) error {
		if len(rawParams) != 1 {
			return fmt.Errorf("invalid param count: expected 1, got %d", len(rawParams))
		}
		var req Req
		if err := decodeParam(rawParams[0], &req); err != nil {
			return fmt.Errorf("param 0 unmarshal error: %w", err)
		}
		return fn(ctx, req, &Stream[Item]{
			ctx: ctx,
			send: func(item Item) error {
				return sendChunk(item)
			},
		})
	}
	return h
}

func (h *Handler) RegisterStreamNoArg[Item any](name string, fn func(ctx context.Context, stream *Stream[Item]) error) *Handler {
	h.streamRoutesMu.Lock()
	defer h.streamRoutesMu.Unlock()
	h.streamRoutes[name] = func(ctx context.Context, rawParams []any, sendChunk func(data any) error) error {
		if len(rawParams) != 0 {
			return fmt.Errorf("invalid param count: expected 0, got %d", len(rawParams))
		}
		return fn(ctx, &Stream[Item]{
			ctx: ctx,
			send: func(item Item) error {
				return sendChunk(item)
			},
		})
	}
	return h
}

func (h *Handler) Broadcast[T any](topic string, data T) error {
	msg := map[string]any{
		"topic": topic,
		"data":  data,
	}
	h.connsMu.RLock()
	defer h.connsMu.RUnlock()

	var firstErr error
	for conn := range h.conns {
		if err := conn.SendJSON(msg); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *Handler) addConn(conn *wsConn) {
	h.connsMu.Lock()
	h.conns[conn] = struct{}{}
	h.connsMu.Unlock()
}

func (h *Handler) removeConn(conn *wsConn) {
	h.connsMu.Lock()
	delete(h.conns, conn)
	h.connsMu.Unlock()
}

func (h *Handler) HttpHandler() http.Handler {
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	opts := &coderWS.AcceptOptions{InsecureSkipVerify: true}
	if h.compressionEnabled {
		opts.CompressionMode = coderWS.CompressionContextTakeover
		opts.CompressionThreshold = h.compressionThreshold
	} else {
		opts.CompressionMode = coderWS.CompressionDisabled
	}

	conn, err := coderWS.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(coderWS.StatusNormalClosure, "")

	cConn := &wsConn{conn: conn}
	h.addConn(cConn)
	defer h.removeConn(cConn)

	streams := &streamTracker{cancels: make(map[string]context.CancelFunc)}
	defer streams.cancelAll()

	var apiVal reflect.Value
	if h.apiStruct != nil {
		apiVal = reflect.ValueOf(h.apiStruct)
	}

	ctx := r.Context()
	for {
		var req wsRPCRequest
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			break
		}
		h.handleRequest(ctx, cConn, req, streams, apiVal)
	}
}

func (h *Handler) handleRequest(
	baseCtx context.Context,
	conn *wsConn,
	req wsRPCRequest,
	streams *streamTracker,
	apiVal reflect.Value,
) {
	if req.Cancel {
		streams.cancel(req.ID)
		return
	}

	if h.isTokenValid != nil && !h.isTokenValid(req.Token, req.Method, req.Params) {
		_ = conn.SendJSON(map[string]any{
			"id":    req.ID,
			"error": "token not valid",
		})
		return
	}

	h.streamRoutesMu.RLock()
	streamInvoker, isStream := h.streamRoutes[req.Method]
	h.streamRoutesMu.RUnlock()

	if isStream {
		streamCtx, streamCancel := context.WithCancel(baseCtx)
		streams.add(req.ID, streamCancel)

		go func() {
			defer streams.cancel(req.ID)

			sendChunk := func(data any) error {
				return conn.SendJSON(map[string]any{
					"id":     req.ID,
					"stream": true,
					"done":   false,
					"result": map[string]any{
						"data":  data,
						"error": nil,
					},
				})
			}

			var streamErr error
			func() {
				defer func() {
					if r := recover(); r != nil {
						streamErr = fmt.Errorf("stream panic: %v", r)
					}
				}()
				streamErr = streamInvoker(streamCtx, req.Params, sendChunk)
			}()

			var errVal any
			if streamErr != nil && streamCtx.Err() == nil {
				errVal = streamErr.Error()
				log.Println(errVal)
			}

			_ = conn.SendJSON(map[string]any{
				"id":     req.ID,
				"stream": true,
				"done":   true,
				"result": map[string]any{
					"data":  nil,
					"error": errVal,
				},
			})
		}()
		return
	}

	h.routesMu.RLock()
	invoker, ok := h.routes[req.Method]
	h.routesMu.RUnlock()

	if ok {
		var result any
		var errVal any
		func() {
			defer func() {
				if r := recover(); r != nil {
					errVal = fmt.Sprintf("method panic: %v", r)
				}
			}()
			res, err := invoker(context.Background(), req.Params)
			if err != nil {
				errVal = err.Error()
			}
			result = res
		}()

		if errVal != nil {
			log.Println(errVal)
		}

		_ = conn.SendJSON(map[string]any{
			"id": req.ID,
			"result": map[string]any{
				"data":  result,
				"error": errVal,
			},
		})
		return
	}

	if !apiVal.IsValid() {
		_ = conn.SendJSON(map[string]any{
			"id":    req.ID,
			"error": "method not found: " + req.Method,
		})
		return
	}

	method := apiVal.MethodByName(req.Method)
	if !method.IsValid() {
		_ = conn.SendJSON(map[string]any{
			"id":    req.ID,
			"error": "method not found: " + req.Method,
		})
		return
	}

	mType := method.Type()
	numIn := mType.NumIn()
	hasCtx := false
	startIdx := 0

	if numIn > 0 && mType.In(0).Implements(reflect.TypeOf((*context.Context)(nil)).Elem()) {
		hasCtx = true
		startIdx = 1
	}

	expectedParams := numIn - startIdx
	if len(req.Params) != expectedParams {
		_ = conn.SendJSON(map[string]any{
			"id": req.ID,
			"error": fmt.Sprintf(
				"invalid param count: expected %d, got %d",
				expectedParams, len(req.Params),
			),
		})
		return
	}

	var in []reflect.Value
	if hasCtx {
		in = append(in, reflect.ValueOf(context.Background()))
	}

	for i := startIdx; i < numIn; i++ {
		argType := mType.In(i)
		argPtr := reflect.New(argType)
		if err := decodeParam(req.Params[i-startIdx], argPtr.Interface()); err != nil {
			_ = conn.SendJSON(map[string]any{
				"id": req.ID,
				"error": fmt.Sprintf(
					"param %d unmarshal to %s failed: %v",
					i-startIdx, argType.Name(), err,
				),
			})
			return
		}
		in = append(in, argPtr.Elem())
	}

	var out []reflect.Value
	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = conn.SendJSON(map[string]any{
					"id":    req.ID,
					"error": fmt.Sprintf("method panic: %v", r),
				})
				out = nil
			}
		}()
		out = method.Call(in)
	}()
	if out == nil {
		return
	}

	if mType.NumOut() > 0 && mType.Out(0).Kind() == reflect.Chan {
		chVal := out[0]
		var initErr any
		if len(out) > 1 && !out[1].IsNil() {
			if e, ok := out[1].Interface().(error); ok {
				initErr = e.Error()
			} else {
				initErr = "unknown error"
			}
		}

		if initErr != nil {
			_ = conn.SendJSON(map[string]any{
				"id":     req.ID,
				"stream": true,
				"done":   true,
				"result": map[string]any{
					"data":  nil,
					"error": initErr,
				},
			})
			return
		}

		streamCtx, streamCancel := context.WithCancel(baseCtx)
		streams.add(req.ID, streamCancel)

		go func() {
			defer streams.cancel(req.ID)

			cases := []reflect.SelectCase{
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(streamCtx.Done())},
				{Dir: reflect.SelectRecv, Chan: chVal},
			}

			for {
				chosen, recv, recvOK := reflect.Select(cases)
				if chosen == 0 || !recvOK {
					break
				}
				_ = conn.SendJSON(map[string]any{
					"id":     req.ID,
					"stream": true,
					"done":   false,
					"result": map[string]any{
						"data":  recv.Interface(),
						"error": nil,
					},
				})
			}

			_ = conn.SendJSON(map[string]any{
				"id":     req.ID,
				"stream": true,
				"done":   true,
				"result": map[string]any{
					"data":  nil,
					"error": nil,
				},
			})
		}()
		return
	}

	var result any
	var errVal any

	if len(out) > 0 {
		result = out[0].Interface()
	}
	if len(out) > 1 {
		if errIF := out[1].Interface(); errIF != nil {
			if e, ok := errIF.(error); ok {
				errVal = e.Error()
			} else {
				errVal = "unknown error type"
			}
		}
	}

	if errVal != nil {
		log.Println(errVal)
	}

	_ = conn.SendJSON(map[string]any{
		"id": req.ID,
		"result": map[string]any{
			"data":  result,
			"error": errVal,
		},
	})
}

func HttpHandler(apiStruct any, isTokenValid func(token string, method string, params []any) bool) http.Handler {
	return NewHandler().SetAPIStruct(apiStruct).SetTokenValidator(isTokenValid)
}
