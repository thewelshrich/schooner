package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
)

func TestVerifyProbesAccountAndWritePermission(t *testing.T) {
	var created, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dop_v1_test-token-abcdefghijklmnopqrstuvwxyz" {
			t.Errorf("authorization header missing")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/account":
			writeJSON(w, `{"account":{"email":"dev@example.com","team":{"uuid":"team-1","name":"Personal"}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/regions":
			writeJSON(w, `{"regions":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/sizes":
			writeJSON(w, `{"sizes":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/images":
			writeJSON(w, `{"images":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/vpcs":
			writeJSON(w, `{"vpcs":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/account/keys":
			writeJSON(w, `{"ssh_keys":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/account/keys":
			created = true
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, `{"ssh_key":{"id":7,"name":"probe"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/account/keys/7":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cloud := New()
	cloud.baseURL = server.URL + "/"
	account, err := cloud.Verify(t.Context(), "dop_v1_test-token-abcdefghijklmnopqrstuvwxyz")
	if err != nil || account.ExternalID != "team-1" || !created || !deleted {
		t.Fatalf("account=%+v created=%t deleted=%t err=%v", account, created, deleted, err)
	}
}

func TestProvisionReconcilesCreatesAndDestroysTaggedDroplet(t *testing.T) {
	var mu sync.Mutex
	exists := false
	createCount := 0
	var createBody map[string]any
	createdKeyNames := map[string]int{}
	deletedKeyIDs := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/regions":
			writeJSON(w, `{"regions":[{"slug":"fra1","name":"Frankfurt","available":true}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/sizes":
			writeJSON(w, `{"sizes":[{"slug":"s-1vcpu-1gb","description":"Basic","memory":1024,"vcpus":1,"disk":25,"price_monthly":6,"regions":["fra1"],"available":true}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/images":
			writeJSON(w, `{"images":[{"slug":"ubuntu-24-04-x64","name":"Ubuntu 24.04","distribution":"Ubuntu","regions":["fra1"]}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/vpcs":
			writeJSON(w, `{"vpcs":[{"id":"vpc-1","name":"default-fra1","region":"fra1"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/account/keys":
			writeJSON(w, `{"ssh_keys":[{"id":9,"name":"laptop","fingerprint":"fp","public_key":"ssh-ed25519 AAAA laptop"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/account/keys":
			var body struct {
				Name      string `json:"name"`
				PublicKey string `json:"public_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			id := 10 + len(createdKeyNames)
			createdKeyNames[body.Name] = id
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, fmt.Sprintf(`{"ssh_key":{"id":%d,"name":%q,"public_key":%q}}`, id, body.Name, body.PublicKey))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/account/keys/"):
			deletedKeyIDs[strings.TrimPrefix(r.URL.Path, "/v2/account/keys/")] = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/droplets":
			if exists {
				writeJSON(w, dropletJSON())
			} else {
				writeJSON(w, `{"droplets":[]}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v2/droplets":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			exists = true
			createCount++
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, `{"droplet":{"id":42,"name":"work","tags":["schooner","schooner-op-op-1"]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/droplets/42":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, `{"id":"not_found","message":"not found"}`)
			} else {
				writeJSON(w, `{"droplet":{"id":42,"name":"work","tags":["schooner","schooner-op-op-1"],"networks":{"v4":[{"ip_address":"203.0.113.8","type":"public"}]}}}`)
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/droplets/42":
			exists = false
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cloud := New()
	cloud.baseURL = server.URL + "/"
	cloud.wait = func(context.Context, time.Duration) error { return nil }
	request := provider.ProvisionRequest{Name: "work", CorrelationID: "op-1", Region: "fra1", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64", NetworkID: "vpc-1", AccessKeyIDs: []string{"9"}, LocalPublicKeys: []provider.PublicKey{{Name: "id_ed25519", Fingerprint: "SHA256:local", PublicKey: "ssh-ed25519 CCCC laptop"}}, IPv6: true, ControlPublicKey: "ssh-ed25519 BBBB Schooner"}
	machine, err := cloud.Provision(t.Context(), "dop_v1_test-token-abcdefghijklmnopqrstuvwxyz", request)
	if err != nil {
		t.Fatal(err)
	}
	if machine.ResourceID != "42" || machine.PublicIPv4 != "203.0.113.8" {
		t.Fatalf("machine=%+v", machine)
	}
	if !containsStringSlice(createBody["tags"], "schooner-op-op-1") || createBody["vpc_uuid"] != "vpc-1" {
		t.Fatalf("create body=%v", createBody)
	}
	if createdKeyNames["schooner-op-1"] != 10 || createdKeyNames["schooner-op-1-local-1"] != 11 || !deletedKeyIDs["10"] || !deletedKeyIDs["11"] {
		t.Fatalf("created keys=%v deleted keys=%v", createdKeyNames, deletedKeyIDs)
	}
	if got := fmt.Sprint(createBody["ssh_keys"]); !strings.Contains(got, "9") || !strings.Contains(got, "10") || !strings.Contains(got, "11") {
		t.Fatalf("Droplet SSH keys = %v", createBody["ssh_keys"])
	}
	mu.Lock()
	exists = false // Simulate deletion in the DigitalOcean control panel.
	mu.Unlock()
	request.KnownResourceID = "42"
	if _, err = cloud.Provision(t.Context(), "dop_v1_test-token-abcdefghijklmnopqrstuvwxyz", request); err != nil {
		t.Fatalf("resume after external deletion: %v", err)
	}
	if createCount != 2 {
		t.Fatalf("create count = %d, want a safe replacement", createCount)
	}
	ref := provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: "42", CorrelationID: "op-1", Profile: "digitalocean/personal"}
	if err = cloud.Destroy(t.Context(), "dop_v1_test-token-abcdefghijklmnopqrstuvwxyz", ref); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("droplet survived destroy")
	}
}

func TestProvisionRecoversWithoutCreationCatalogue(t *testing.T) {
	for _, tc := range []struct {
		name, knownID, tags, wantCode string
		correlated, exists            bool
	}{
		{name: "correlated", correlated: true, exists: true},
		{name: "known and correlated", knownID: "42", correlated: true, exists: true},
		{name: "known only", knownID: "42", exists: true},
		{name: "different recorded ID", knownID: "43", correlated: true, exists: true, wantCode: "conflict"},
		{name: "missing correlation tag", knownID: "42", exists: true, tags: "schooner", wantCode: "conflict"},
		{name: "new resource needs catalogue", wantCode: "invalid_input"},
		{name: "deleted resource needs catalogue", knownID: "42", wantCode: "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected mutation", http.StatusBadRequest)
					return
				}
				switch r.URL.Path {
				case "/v2/droplets":
					if tc.correlated {
						writeJSON(w, dropletJSON())
					} else {
						writeJSON(w, `{"droplets":[]}`)
					}
				case "/v2/droplets/42":
					if !tc.exists {
						http.NotFound(w, r)
						return
					}
					tags := tc.tags
					if tags == "" {
						tags = "schooner-op-op-1"
					}
					writeJSON(w, fmt.Sprintf(`{"droplet":{"id":42,"tags":[%q],"networks":{"v4":[{"ip_address":"203.0.113.8","type":"public"}]}}}`, tags))
				case "/v2/account/keys":
					// The originally selected key has been removed.
					writeJSON(w, `{"ssh_keys":[]}`)
				case "/v2/regions", "/v2/sizes", "/v2/images", "/v2/vpcs":
					if tc.exists {
						t.Errorf("recovery consulted creation catalogue: %s", r.URL.Path)
					}
					writeJSON(w, `{}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cloud := New()
			cloud.baseURL = server.URL + "/"
			request := provider.ProvisionRequest{Name: "work", CorrelationID: "op-1", KnownResourceID: tc.knownID, Region: "fra1", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64", AccessKeyIDs: []string{"9"}, ControlPublicKey: "ssh-ed25519 BBBB Schooner"}
			machine, err := cloud.Provision(t.Context(), "token", request)
			if (err == nil) != (tc.wantCode == "") || (err != nil && box.ErrorCode(err) != tc.wantCode) {
				t.Fatalf("machine=%+v err=%v, want code %q", machine, err, tc.wantCode)
			}
			if err == nil && (machine.ResourceID != "42" || machine.PublicIPv4 != "203.0.113.8") {
				t.Fatalf("machine=%+v", machine)
			}
		})
	}
}

func TestProvisionRejectsInvalidKeysBeforeReconciliation(t *testing.T) {
	for _, request := range []provider.ProvisionRequest{
		{AccessKeyIDs: make([]string, 16)},
		{AccessKeyIDs: []string{"9", "9"}},
		{LocalPublicKeys: []provider.PublicKey{{Name: "invalid"}}},
		{LocalPublicKeys: []provider.PublicKey{
			{Name: "one", Fingerprint: "same", PublicKey: "ssh-ed25519 AAAA"},
			{Name: "two", Fingerprint: "same", PublicKey: "ssh-ed25519 AAAA"},
		}},
	} {
		cloud := New()
		// Any attempted reconciliation must fail without reaching a server.
		cloud.baseURL = "invalid://unused/"
		_, err := cloud.Provision(t.Context(), "token", request)
		if box.ErrorCode(err) != "invalid_input" {
			t.Fatalf("request=%+v err=%v", request, err)
		}
	}
}

func TestInspectRejectsMissingCorrelationTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"droplet":{"id":42,"name":"work","tags":["schooner"]}}`)
	}))
	defer server.Close()
	cloud := New()
	cloud.baseURL = server.URL + "/"
	_, err := cloud.Inspect(t.Context(), "token", provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: "42", CorrelationID: "op-1", Profile: "digitalocean/personal"})
	if box.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
}

func TestControlKeyReusesMatchingAccountKeyWithoutOwningIt(t *testing.T) {
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		writeJSON(w, `{"ssh_keys":[{"id":9,"name":"laptop","public_key":"ssh-ed25519 BBBB laptop"}]}`)
	}))
	defer server.Close()
	cloud := New()
	cloud.baseURL = server.URL + "/"
	key, owned, err := ensureControlKey(t.Context(), cloud.client("token"), "op-1", "ssh-ed25519 BBBB Schooner")
	if err != nil || key.ID != 9 || owned || posted {
		t.Fatalf("key=%+v owned=%t posted=%t err=%v", key, owned, posted, err)
	}
}

func TestDestroyClassifiesLostDeleteResponseAsOutcomeUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			panic(http.ErrAbortHandler)
		}
		writeJSON(w, `{"droplet":{"id":42,"name":"work","tags":["schooner-op-op-1"]}}`)
	}))
	defer server.Close()
	cloud := New()
	cloud.baseURL = server.URL + "/"
	err := cloud.Destroy(t.Context(), "token", provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: "42", CorrelationID: "op-1", Profile: "digitalocean/personal"})
	if box.ErrorCode(err) != "outcome_unknown" {
		t.Fatalf("err=%v code=%s", err, box.ErrorCode(err))
	}
}

func dropletJSON() string {
	return `{"droplets":[{"id":42,"name":"work","tags":["schooner","schooner-op-op-1"],"networks":{"v4":[{"ip_address":"203.0.113.8","type":"public"}]}}]}`
}
func writeJSON(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(value))
}
func containsStringSlice(value any, target string) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func TestNoTokenInProviderErrors(t *testing.T) {
	token := "dop_v1_super-secret-token-abcdefghijklmnopqrstuvwxyz"
	err := classify(context.DeadlineExceeded, "test")
	if strings.Contains(err.Error(), token) {
		t.Fatal("token leaked")
	}
}
