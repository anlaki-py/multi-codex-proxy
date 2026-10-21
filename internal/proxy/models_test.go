package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// getModelIDs drives GET /v1/models against a fake upstream catalog and
// returns the ids in order.
func getModelIDs(t *testing.T, catalog string) []string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalog))
	}))
	defer up.Close()

	s := testServerWithAccount(t, up.Client())
	s.baseURL = up.URL
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids
}

func TestModelsFiltersHiddenAndAppendsExtras(t *testing.T) {
	cases := []struct {
		name    string
		catalog string
		want    []string
	}{
		{
			name:    "extra appended when upstream omits it",
			catalog: `{"models":[{"slug":"gpt-5.6-terra","visibility":"list"},{"slug":"gpt-reserve","visibility":"hide"}]}`,
			want:    []string{"gpt-5.6-terra", "gpt-6-luna"},
		},
		{
			name:    "extra not duplicated when upstream hides it",
			catalog: `{"models":[{"slug":"gpt-5.6-terra","visibility":"list"},{"slug":"gpt-6-luna","visibility":"hide"}]}`,
			want:    []string{"gpt-5.6-terra", "gpt-6-luna"},
		},
		{
			name:    "extra not duplicated when upstream lists it",
			catalog: `{"models":[{"slug":"gpt-6-luna","visibility":"list"},{"slug":"gpt-5.6-terra","visibility":"list"}]}`,
			want:    []string{"gpt-6-luna", "gpt-5.6-terra"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := getModelIDs(t, c.catalog); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
