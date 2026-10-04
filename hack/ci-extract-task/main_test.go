package main

import (
	"encoding/json"
	"testing"
)

func TestWithCPURequestReplacesOnlyTheCPU(t *testing.T) {
	in := []byte(`{"platform":"linux","container_requests":{"cpu":6000,"memory":"14GB"},"params":{"A":"1"}}`)
	out, err := withCPURequest(in, 14000)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Platform string            `json:"platform"`
		Params   map[string]string `json:"params"`
		Requests struct {
			CPU    uint64 `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"container_requests"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Requests.CPU != 14000 || got.Requests.Memory != "14GB" || got.Platform != "linux" || got.Params["A"] != "1" {
		t.Fatalf("got %s", out)
	}
}

func TestWithCPURequestAddsTheBlockWhenAbsent(t *testing.T) {
	out, err := withCPURequest([]byte(`{"platform":"linux"}`), 9000)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"container_requests":{"cpu":9000},"platform":"linux"}` {
		t.Fatalf("got %s", out)
	}
}

func TestWithParamsKeepsThePipelinesOwn(t *testing.T) {
	out, err := withParams([]byte(`{"params":{"A":"1"}}`), map[string]string{"UNIT_PACKAGES": "x y"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"params":{"A":"1","UNIT_PACKAGES":"x y"}}` {
		t.Fatalf("got %s", out)
	}
}
