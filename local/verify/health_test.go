//go:build e2e

package verify

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHealthz replaces the old grpc.health.v1.Health check. This is
// literally the kubelet probe path (see internal/mcphttp.HealthPath and
// the chart's readinessProbe/livenessProbe) - the old script asserted the
// gRPC health service reported SERVING, which never actually verified the
// probe endpoint itself, since gRPC health checks and Kubernetes gRPC
// probes are different code paths. This does.
func TestHealthz(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
	}{
		{"skillsd", skillsdAddr()},
		{"skillsd-registry", registryAddr()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get("http://" + tc.addr + "/healthz")
			if err != nil {
				t.Fatalf("GET /healthz on %s: %v", tc.addr, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET /healthz on %s: status %d, body %q", tc.addr, resp.StatusCode, body)
			}
		})
	}
}

// TestInitializeInstructionsNonEmpty replaces the old reflection check
// ("skills.v1.SkillService is listed"). There is no reflection equivalent
// in MCP - the closest analogue is that the server actually completes the
// handshake and hands back onboarding instructions, which is the new
// mechanism replacing the client-guide RPC's role in bootstrapping an
// agent onto this API.
func TestInitializeInstructionsNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
	}{
		{"skillsd", skillsdAddr()},
		{"skillsd-registry", registryAddr()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := connect(t, tc.addr)
			instructions := session.InitializeResult().Instructions
			if len(instructions) < 100 {
				t.Errorf("%s Instructions looks empty or truncated (%d bytes): %q", tc.name, len(instructions), instructions)
			}
		})
	}
}

// TestToolsListNamesExpectedTools replaces the old reflection checks that
// each RPC service was registered ("skills.v1.ProposalService",
// "skills.v1.EvidenceService" show up in `grpcurl list`). tools/list is
// the direct MCP analogue: the set of tools a client actually sees.
func TestToolsListNamesExpectedTools(t *testing.T) {
	t.Run("skillsd", func(t *testing.T) {
		session := connect(t, skillsdAddr())
		got := toolNames(t, session)
		for _, want := range []string{"list_skills", "get_skill", "get_client_guide"} {
			if !got[want] {
				t.Errorf("skillsd tools/list is missing %q; got %v", want, keys(got))
			}
		}
	})

	t.Run("skillsd-registry", func(t *testing.T) {
		session := connect(t, registryAddr())
		got := toolNames(t, session)
		for _, want := range []string{
			"propose_change", "list_proposals", "get_proposal",
			"list_proposal_clusters", "get_skill_at_ref", "submit_proposal",
		} {
			if !got[want] {
				t.Errorf("skillsd-registry tools/list is missing %q; got %v", want, keys(got))
			}
		}
		// The evidence tools are conditional - see TestEvidenceToolsPresence
		// in evidence_test.go, which is the one place this harness treats
		// their absence as expected rather than a failure.
	})
}

func toolNames(t *testing.T, session *mcp.ClientSession) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
