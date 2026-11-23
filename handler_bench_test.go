package rpc

import (
	"bytes"
	"context"
	encodingJson "encoding/json"
	"reflect"
	"testing"
)

type BenchData struct {
	ID       int      `json:"id"`
	Username string   `json:"username"`
	IsActive bool     `json:"is_active"`
	Roles    []string `json:"roles"`
	Score    float64  `json:"score"`
}

type BenchService struct{}

func (s *BenchService) ProcessData(ctx context.Context, d BenchData) (BenchData, error) {
	d.Score += 1.0
	return d, nil
}

// Benchmark 1: Go 1.27 Generic Method Dispatch vs Dynamic Reflection Dispatch
func BenchmarkDispatch_Generic(b *testing.B) {
	h := NewHandler()
	Register(h, "process", func(ctx context.Context, req BenchData) (BenchData, error) {
		req.Score += 1.0
		return req, nil
	})

	invoker := h.routes["process"]
	rawParams := []any{
		map[string]any{
			"id":        42,
			"username":  "johndoe",
			"is_active": true,
			"roles":     []any{"admin", "user"},
			"score":     98.5,
		},
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := invoker(ctx, rawParams)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDispatch_Reflection(b *testing.B) {
	svc := &BenchService{}
	val := reflect.ValueOf(svc)
	method := val.MethodByName("ProcessData")
	mType := method.Type()
	ctxType := reflect.TypeFor[context.Context]()

	rawParams := []any{
		map[string]any{
			"id":        42,
			"username":  "johndoe",
			"is_active": true,
			"roles":     []any{"admin", "user"},
			"score":     98.5,
		},
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		hasCtx := mType.In(0).Implements(ctxType)
		var in []reflect.Value
		if hasCtx {
			in = append(in, reflect.ValueOf(ctx))
		}

		argType := mType.In(1)
		argPtr := reflect.New(argType)
		rawBytes, _ := json.Marshal(rawParams[0])
		_ = json.NewDecoder(bytes.NewReader(rawBytes)).Decode(argPtr.Interface())
		in = append(in, argPtr.Elem())

		out := method.Call(in)
		_ = out[0].Interface()
	}
}

// Benchmark 2: Standard encoding/json vs json-iterator Decoder and Encoder
func BenchmarkJSON_Decoder_Stdlib(b *testing.B) {
	sample := []byte(`{"id":"req-1","method":"process","params":[{"id":42,"username":"johndoe","is_active":true,"roles":["admin","user"],"score":98.5}],"token":"token-xyz"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
			Token  string `json:"token"`
		}
		dec := encodingJson.NewDecoder(bytes.NewReader(sample))
		if err := dec.Decode(&req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSON_Decoder_Jsoniter(b *testing.B) {
	sample := []byte(`{"id":"req-1","method":"process","params":[{"id":42,"username":"johndoe","is_active":true,"roles":["admin","user"],"score":98.5}],"token":"token-xyz"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
			Token  string `json:"token"`
		}
		dec := json.NewDecoder(bytes.NewReader(sample))
		if err := dec.Decode(&req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSON_Encoder_Stdlib(b *testing.B) {
	resp := map[string]any{
		"id": "req-1",
		"result": map[string]any{
			"data": BenchData{
				ID:       42,
				Username: "johndoe",
				IsActive: true,
				Roles:    []string{"admin", "user"},
				Score:    99.5,
			},
			"error": nil,
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := encodingJson.Marshal(resp)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSON_Encoder_Jsoniter(b *testing.B) {
	resp := map[string]any{
		"id": "req-1",
		"result": map[string]any{
			"data": BenchData{
				ID:       42,
				Username: "johndoe",
				IsActive: true,
				Roles:    []string{"admin", "user"},
				Score:    99.5,
			},
			"error": nil,
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(resp)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark 3: Streaming chunks vs Full Batch response (100 items)
func BenchmarkDataDelivery_Streaming(b *testing.B) {
	ctx := context.Background()
	items := make([]BenchData, 100)
	for i := range 100 {
		items[i] = BenchData{
			ID:       i,
			Username: "user",
			IsActive: true,
			Roles:    []string{"role"},
			Score:    float64(i),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stream := &Stream[BenchData]{
			ctx: ctx,
			send: func(item BenchData) error {
				// Simulates serializing and transmitting chunk
				_, err := json.Marshal(item)
				return err
			},
		}
		for _, item := range items {
			_ = stream.Send(item)
		}
	}
}

func BenchmarkDataDelivery_Batch(b *testing.B) {
	items := make([]BenchData, 100)
	for i := range 100 {
		items[i] = BenchData{
			ID:       i,
			Username: "user",
			IsActive: true,
			Roles:    []string{"role"},
			Score:    float64(i),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Simulates serializing and transmitting complete batch
		_, _ = json.Marshal(items)
	}
}
