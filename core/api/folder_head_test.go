package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestFolderTrashOnlyFallsBackOnOlderServer(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		trash, account, writes, want int
		body                         string
	}{
		{"conflict never downgrades", 409, 404, 1, 409, `{}`},
		{"modern refusal never downgrades", 400, 200, 1, 400, `{"user_id":"u1","user":"alice"}`},
		{"unavailable probe never downgrades", 400, 503, 1, 503, `{}`},
		{"malformed probe never downgrades", 400, 200, 1, 0, `{}`},
		{"old strict JSON server", 400, 404, 2, 0, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes, probes := 0, 0
			c := client(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer kyd_secret" {
					t.Error("missing credential")
				}
				if r.URL.Path == "/api/drive/device" {
					probes++
					w.WriteHeader(tc.account)
					io.WriteString(w, tc.body)
					return
				}
				writes++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if writes == 1 {
					if body["head"] != float64(7) {
						t.Errorf("expected head missing: %+v", body)
					}
					w.WriteHeader(tc.trash)
					io.WriteString(w, `{"error":"refused"}`)
				} else if _, ok := body["head"]; ok {
					t.Error("legacy request kept unknown head")
				}
			})
			err := c.TrashFolder(t.Context(), "folder", 7)
			if writes != tc.writes || Status(err) != tc.want {
				t.Fatalf("writes=%d, error=%v", writes, err)
			}
			if tc.name == "malformed probe never downgrades" && err == nil {
				t.Fatal("malformed identity accepted")
			}
			if tc.trash == 409 && probes != 0 {
				t.Fatal("409 performed a compatibility probe")
			}
		})
	}
}
