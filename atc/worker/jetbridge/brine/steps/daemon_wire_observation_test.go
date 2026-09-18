package steps

import (
	"net/http"
	"testing"
)

func TestDaemonObservationRestoresDefaultTransport(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "normal"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			original := http.DefaultTransport
			constructed := false
			func() {
				defer func() {
					got := recover()
					if panics && got != "constructor failed" {
						t.Errorf("expected constructor panic, got %v", got)
					}
					if !panics && got != nil {
						t.Errorf("unexpected panic: %v", got)
					}
				}()
				trace, err := observeDaemonConstruction(nil, func() {
					constructed = true
					if http.DefaultTransport == original {
						t.Error("observation not installed during construction")
					}
					if panics {
						panic("constructor failed")
					}
				})
				if err != nil || trace == nil {
					t.Errorf("observation: %v, %v", trace, err)
				}
			}()
			if !constructed {
				t.Error("constructor never called")
			}
			if http.DefaultTransport != original {
				t.Fatal("default transport was not restored")
			}
		})
	}
}
