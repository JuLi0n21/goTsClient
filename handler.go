package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"

	"golang.org/x/net/websocket"
)

func WsHandler(apiStruct any, isTokenValid func(token string, method string, params []any) bool) func(*websocket.Conn) {
	return func(ws *websocket.Conn) {
		apiVal := reflect.ValueOf(apiStruct)

		for {
			var msg []byte
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				break
			}

			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
				Params []any  `json:"params"`
				Token  string `json:"token"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				_ = websocket.JSON.Send(ws, map[string]any{
					"id":    "",
					"error": "invalid json payload",
				})
				continue
			}

			if !isTokenValid(req.Token, req.Method, req.Params) {
				_ = websocket.JSON.Send(ws, map[string]any{
					"id":    req.ID,
					"error": "token not valid",
				})
				return
			}

			method := apiVal.MethodByName(req.Method)
			if !method.IsValid() {
				_ = websocket.JSON.Send(ws, map[string]any{
					"id":    req.ID,
					"error": "method not found: " + req.Method,
				})
				continue
			}

			mType := method.Type()
			numIn := mType.NumIn()

			hasCtx := false
			startIdx := 0
			ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()

			if numIn > 0 && mType.In(0).Implements(ctxType) {
				hasCtx = true
				startIdx = 1
			}

			expectedParams := numIn - startIdx
			if len(req.Params) != expectedParams {
				_ = websocket.JSON.Send(ws, map[string]any{
					"id": req.ID,
					"error": fmt.Sprintf(
						"invalid param count: expected %d, got %d",
						expectedParams, len(req.Params),
					),
				})
				continue
			}

			var in []reflect.Value

			if hasCtx {
				ctx := context.Background()
				in = append(in, reflect.ValueOf(ctx))
			}

			valid := true
			for i := startIdx; i < numIn; i++ {
				argType := mType.In(i)
				raw := req.Params[i-startIdx]

				argPtr := reflect.New(argType)
				b, err := json.Marshal(raw)
				if err != nil {
					_ = websocket.JSON.Send(ws, map[string]any{
						"id":    req.ID,
						"error": fmt.Sprintf("param %d marshal error: %v", i-startIdx, err),
					})
					valid = false
					break
				}
				if err := json.Unmarshal(b, argPtr.Interface()); err != nil {
					_ = websocket.JSON.Send(ws, map[string]any{
						"id": req.ID,
						"error": fmt.Sprintf(
							"param %d unmarshal to %s failed: %v",
							i-startIdx, argType.Name(), err,
						),
					})
					valid = false
					break
				}

				in = append(in, argPtr.Elem())
			}

			if !valid {
				continue
			}

			var out []reflect.Value
			func() { // protect against panics
				defer func() {
					if r := recover(); r != nil {
						_ = websocket.JSON.Send(ws, map[string]any{
							"id":    req.ID,
							"error": fmt.Sprintf("method panic: %v", r),
						})
						out = nil
					}
				}()
				out = method.Call(in)
			}()
			if out == nil {
				continue
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

			_ = websocket.JSON.Send(ws, map[string]any{
				"id": req.ID,
				"result": map[string]any{
					"data":  result,
					"error": errVal,
				},
			})
		}
	}
}
