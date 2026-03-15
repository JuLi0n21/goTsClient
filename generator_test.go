package rpc

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	IsActive bool   `json:"is_active"`
	PtrField *int   `json:"ptr_field"`
}

type Order struct {
	OrderID int    `json:"order_id"`
	Items   []Item `json:"items"`
}

type Item struct {
	Name string `json:"name"`
}

type MockAPI struct{}

func (s *MockAPI) GetUser(id int) (User, error) { return User{}, nil }
func (s *MockAPI) CreateOrder(o Order) error    { return nil }
func (s *MockAPI) NoArgs() error                { return nil }

func TestGoTypeToTS(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{"Int", 42, "number"},
		{"String", "hello", "string"},
		{"Bool", true, "boolean"},
		{"Slice", []string{}, "string[]"},
		{"Byte Slice", []byte{}, "string"},
		{"Pointer", new(int), "number"},
		{"Map", map[string]int{}, "{ [key: string]: number }"},
		{"Struct", User{}, "User"},
		{"Anon Struct", struct {
			Name string `json:"name"`
		}{Name: "test"}, "{ name: string }"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ := reflect.TypeOf(tt.input)
			got := goTypeToTS(typ)
			if got != tt.expected {
				t.Errorf("goTypeToTS() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCollectStructs(t *testing.T) {
	structs := make(map[string]reflect.Type)

	// Test nested collection
	collectStructs(reflect.TypeOf(Order{}), structs)

	if _, ok := structs["Order"]; !ok {
		t.Error("Expected Order in map")
	}
	if _, ok := structs["Item"]; !ok {
		t.Error("Expected Item (nested) in map")
	}
}

func TestGenerateTS_Success(t *testing.T) {
	api := &MockAPI{}
	output, err := GenerateTS(reflect.TypeOf(api), "TestClient")
	if err != nil {
		t.Fatalf("GenerateTS failed: %v", err)
	}

	expectedStrings := []string{
		"export interface User {",
		"id: number;",
		"is_active: boolean;",
		"export interface TestClient {",
		"GetUser(arg0: number): Promise<RPCResult<User>>;",
		"CreateOrder(order: Order): Promise<RPCResult<any>>;",
		"useBackend(url: string = '/ws')",
	}

	for _, s := range expectedStrings {
		if !strings.Contains(output, s) {
			t.Errorf("Generated TS missing expected string: %s", s)
		}
	}
}

type APIWithTooManyReturns struct{}

func (s *APIWithTooManyReturns) Bad(id int) (int, int, error) { return 0, 0, nil }

type APIWithNoErrorHandler struct{}

func (s *APIWithNoErrorHandler) Bad(id int) int { return 0 }

func TestGenerateTS_Validation(t *testing.T) {
	type BadAPI struct{}

	t.Run("InvalidReturnCount", func(t *testing.T) {
		api := &APIWithTooManyReturns{}
		_, err := GenerateTS(reflect.TypeOf(api), "Client")
		if err == nil {
			t.Error("Expected error for method with 3 return values")
		}
	})

	t.Run("NoResidentError", func(t *testing.T) {
		api := &APIWithNoErrorHandler{}
		_, err := GenerateTS(reflect.TypeOf(api), "Client")
		if err == nil {
			t.Error("Expected error for method missing 'error' return type")
		}
	})
}

func TestGenClient(t *testing.T) {
	tmpFile := "test_client.ts"
	defer os.Remove(tmpFile)

	api := &MockAPI{}
	err := GenClient(api, tmpFile)
	if err != nil {
		t.Fatalf("GenClient failed: %v", err)
	}

	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Error("GenClient did not create the file")
	}
}
