package gotsclient

import (
	"fmt"
	"testing"
)

func TestGenerateClient(t *testing.T) {

	fmt.Println(GenClient(Api{}, "./client.ts"))
}
