package rpc

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderWS "github.com/coder/websocket"
	coderWSJson "github.com/coder/websocket/wsjson"
)

type TestReq struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type TestResp struct {
	Greeting string `json:"greeting"`
	IsAdult  bool   `json:"is_adult"`
}

type TestAlert struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func TestHandler_GenericRegister(t *testing.T) {
	h := NewHandler()

	h.Register("greet", func(ctx context.Context, req TestReq) (TestResp, error) {
		return TestResp{
			Greeting: "Hello " + req.Name,
			IsAdult:  req.Age >= 18,
		}, nil
	})

	h.RegisterNoArg("status", func(ctx context.Context) (string, error) {
		return "ok", nil
	})

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	reqMsg := map[string]any{
		"id":     "1",
		"method": "greet",
		"params": []any{
			map[string]any{"name": "Alice", "age": 25},
		},
	}
	if err := coderWSJson.Write(ctx, ws, reqMsg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp struct {
		ID     string `json:"id"`
		Result struct {
			Data  TestResp `json:"data"`
			Error any      `json:"error"`
		} `json:"result"`
	}
	if err := coderWSJson.Read(ctx, ws, &resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.ID != "1" {
		t.Errorf("expected ID '1', got %q", resp.ID)
	}
	if resp.Result.Data.Greeting != "Hello Alice" || !resp.Result.Data.IsAdult {
		t.Errorf("unexpected result: %+v", resp.Result.Data)
	}

	reqMsg2 := map[string]any{
		"id":     "2",
		"method": "status",
		"params": []any{},
	}
	if err := coderWSJson.Write(ctx, ws, reqMsg2); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp2 struct {
		ID     string `json:"id"`
		Result struct {
			Data  string `json:"data"`
			Error any    `json:"error"`
		} `json:"result"`
	}
	if err := coderWSJson.Read(ctx, ws, &resp2); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp2.Result.Data != "ok" {
		t.Errorf("expected 'ok', got %q", resp2.Result.Data)
	}
}

func TestHandler_GenericBroadcast(t *testing.T) {
	h := NewHandler()

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(1)

	var received struct {
		Topic string    `json:"topic"`
		Data  TestAlert `json:"data"`
	}

	go func() {
		defer wg.Done()
		_ = coderWSJson.Read(ctx, ws, &received)
	}()

	err = h.Broadcast("system_alert", TestAlert{
		Level:   "critical",
		Message: "high load detected",
	})
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}

	wg.Wait()

	if received.Topic != "system_alert" {
		t.Errorf("expected topic 'system_alert', got %q", received.Topic)
	}
	if received.Data.Level != "critical" || received.Data.Message != "high load detected" {
		t.Errorf("unexpected broadcast data: %+v", received.Data)
	}
}

func TestHandler_GenericStream(t *testing.T) {
	h := NewHandler()

	type CountReq struct {
		Count int `json:"count"`
	}

	h.RegisterStream("countUp", func(ctx context.Context, req CountReq, stream *Stream[int]) error {
		for i := 1; i <= req.Count; i++ {
			if err := stream.Send(i); err != nil {
				return err
			}
		}
		return nil
	})

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	reqMsg := map[string]any{
		"id":     "stream-1",
		"method": "countUp",
		"params": []any{
			CountReq{Count: 3},
		},
	}
	if err := coderWSJson.Write(ctx, ws, reqMsg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var receivedNumbers []int
	var finished bool

	type StreamMsg struct {
		ID     string `json:"id"`
		Stream bool   `json:"stream"`
		Done   bool   `json:"done"`
		Result struct {
			Data  int `json:"data"`
			Error any `json:"error"`
		} `json:"result"`
	}

	for !finished {
		var sMsg StreamMsg
		if err := coderWSJson.Read(ctx, ws, &sMsg); err != nil {
			t.Fatalf("Read failed: %v", err)
		}

		if !sMsg.Stream {
			t.Errorf("expected stream: true")
		}

		if sMsg.Done {
			finished = true
		} else {
			receivedNumbers = append(receivedNumbers, sMsg.Result.Data)
		}
	}

	if len(receivedNumbers) != 3 || receivedNumbers[0] != 1 || receivedNumbers[1] != 2 || receivedNumbers[2] != 3 {
		t.Errorf("unexpected streamed numbers: %v", receivedNumbers)
	}
}

func TestHandler_StreamCancel(t *testing.T) {
	h := NewHandler()

	cancelledCh := make(chan struct{})

	h.RegisterStreamNoArg("infinite", func(ctx context.Context, stream *Stream[string]) error {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stream.Context().Done():
				close(cancelledCh)
				return stream.Context().Err()
			case <-ticker.C:
				if err := stream.Send("tick"); err != nil {
					return err
				}
			}
		}
	})

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	_ = coderWSJson.Write(ctx, ws, map[string]any{
		"id":     "stream-inf",
		"method": "infinite",
		"params": []any{},
	})

	var tickMsg map[string]any
	if err := coderWSJson.Read(ctx, ws, &tickMsg); err != nil {
		t.Fatalf("Read tick failed: %v", err)
	}

	_ = coderWSJson.Write(ctx, ws, map[string]any{
		"id":     "stream-inf",
		"cancel": true,
	})

	select {
	case <-cancelledCh:
	case <-time.After(1 * time.Second):
		t.Fatal("Stream was not cancelled in time")
	}
}

type MockChannelAPI struct{}

func (s *MockChannelAPI) StreamStrings() <-chan string {
	ch := make(chan string, 3)
	go func() {
		ch <- "first"
		ch <- "second"
		close(ch)
	}()
	return ch
}

func TestHandler_ReflectionChannelStream(t *testing.T) {
	api := &MockChannelAPI{}
	h := NewHandler().SetAPIStruct(api)

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	_ = coderWSJson.Write(ctx, ws, map[string]any{
		"id":     "chan-1",
		"method": "StreamStrings",
		"params": []any{},
	})

	var items []string
	for {
		var sMsg struct {
			Stream bool `json:"stream"`
			Done   bool `json:"done"`
			Result struct {
				Data string `json:"data"`
			} `json:"result"`
		}
		if err := coderWSJson.Read(ctx, ws, &sMsg); err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		if sMsg.Done {
			break
		}
		items = append(items, sMsg.Result.Data)
	}

	if len(items) != 2 || items[0] != "first" || items[1] != "second" {
		t.Errorf("unexpected items from channel stream: %v", items)
	}
}

func TestHandler_ReflectionFallback(t *testing.T) {
	api := &MockAPI{}
	server := httptest.NewServer(HttpHandler(api, func(token string, method string, params []any) bool {
		return token == "secret"
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	ws, _, err := coderWS.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer ws.Close(coderWS.StatusNormalClosure, "")

	reqMsg := map[string]any{
		"id":     "1",
		"method": "NoArgs",
		"params": []any{},
		"token":  "wrong",
	}
	_ = coderWSJson.Write(ctx, ws, reqMsg)

	var errResp struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := coderWSJson.Read(ctx, ws, &errResp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if errResp.Error != "token not valid" {
		t.Errorf("expected 'token not valid', got %q", errResp.Error)
	}
}

func TestHandler_Compression(t *testing.T) {
	h := NewHandler().SetCompression(true, 128)

	type EchoReq struct {
		Payload string `json:"payload"`
	}

	h.Register("echo", func(ctx context.Context, req EchoReq) (EchoReq, error) {
		return req, nil
	})

	server := httptest.NewServer(h)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(server.URL, "http://")
	opts := &coderWS.DialOptions{
		CompressionMode: coderWS.CompressionContextTakeover,
	}
	conn, resp, err := coderWS.Dial(ctx, wsURL, opts)
	if err != nil {
		t.Fatalf("coderWS.Dial failed: %v", err)
	}
	defer conn.Close(coderWS.StatusNormalClosure, "")

	extHeader := resp.Header.Get("Sec-WebSocket-Extensions")
	if !strings.Contains(extHeader, "permessage-deflate") {
		t.Errorf("expected Sec-WebSocket-Extensions to contain 'permessage-deflate', got %q", extHeader)
	}

	largeText := strings.Repeat("repeated-compressible-text-chunk-", 30)
	sendReq := map[string]any{
		"id":     "comp-1",
		"method": "echo",
		"params": []any{
			EchoReq{Payload: largeText},
		},
	}
	if err := coderWSJson.Write(ctx, conn, sendReq); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var echoResp struct {
		ID     string `json:"id"`
		Result struct {
			Data  EchoReq `json:"data"`
			Error any     `json:"error"`
		} `json:"result"`
	}
	if err := coderWSJson.Read(ctx, conn, &echoResp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if echoResp.ID != "comp-1" {
		t.Errorf("expected id 'comp-1', got %q", echoResp.ID)
	}
	if echoResp.Result.Data.Payload != largeText {
		t.Errorf("large text corrupted during compressed roundtrip")
	}
}
