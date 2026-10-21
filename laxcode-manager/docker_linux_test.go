package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDockerListIsNotInspect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			w.Write([]byte(`{"ApiVersion":"1.54"}`))
		case r.URL.Path == "/v1.54/containers/json":
			// Docker list.State is a string, inspect.State is an object.
			w.Write([]byte(`[{"Id":"abc","State":"running","Labels":{"openshell.ai/isolation-role":"staging"}}]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	transport := server.Client().Transport
	d := &dockerClient{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(r)
	})}}
	got, err := d.discover(context.Background(), defaultConfig())
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTrustedWorkplaceIdentity(t *testing.T) {
	id := uuid.NewString()
	sid := uuid.NewString()
	base := `{"Id":"container","Config":{"Labels":{"openshell.ai/managed-by":"openshell","openshell.ai/sandbox-namespace":"laxcode","openshell.ai/sandbox-id":"` + sid + `","openshell.ai/isolation-role":"sandbox","openshell.ai/sandbox-name":"` + userName(id) + `"}},"Mounts":[{"Type":"bind","Source":"/root/laxcode/` + id + `","Destination":"/workplace"}]}`
	var c container
	if err := json.Unmarshal([]byte(base), &c); err != nil {
		t.Fatal(err)
	}
	user, managed, err := workloadUser(c, "/root/laxcode")
	if err != nil || !managed || user != id {
		t.Fatal(user, managed, err)
	}
	if _, _, err = identity(c.Config.Labels, "other"); err == nil {
		t.Fatal("accepted wrong namespace")
	}
	for _, source := range []string{"/root/other/" + id, "/root/laxcode/not-a-uuid", "/root/laxcode/../" + id} {
		c.Mounts[0].Source = source
		if _, _, err = workloadUser(c, "/root/laxcode"); err == nil {
			t.Fatal("accepted source", source)
		}
	}
	c.Mounts[0].Source = "/root/laxcode/" + uuid.NewString()
	if _, _, err = workloadUser(c, "/root/laxcode"); err == nil {
		t.Fatal("accepted name mismatch")
	}
	c.Mounts = nil
	if _, managed, err = workloadUser(c, "/root/laxcode"); err != nil || managed {
		t.Fatal("adopted unrelated sandbox")
	}
}
