package k8s_test

import (
	"os"
	"strings"
	"testing"
)

func TestDeploymentUsesDedicatedHealthProbes(t *testing.T) {
	t.Parallel()

	manifest := readFile(t, "deployment.yaml")
	for _, want := range []string{
		"startupProbe:\n            exec:\n              command: [wget, -qO-, http://127.0.0.1:8080/readyz]",
		"livenessProbe:\n            exec:\n              command: [wget, -qO-, http://127.0.0.1:8080/livez]",
		"readinessProbe:\n            exec:\n              command: [wget, -qO-, http://127.0.0.1:8080/readyz]",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("deployment.yaml missing health probe:\n%s", want)
		}
	}
}

func TestBeyondNetworkPolicyOnlyAdmitsBeyondPods(t *testing.T) {
	t.Parallel()

	manifest := readFile(t, "networkpolicy-beyond.yaml")
	for _, want := range []string{
		"name: pcr-only-from-beyond",
		"namespace: pcr",
		"app: pcr-server",
		"kubernetes.io/metadata.name: beyond",
		"app.kubernetes.io/name: beyond",
		"port: 8080",
		"protocol: TCP",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("networkpolicy-beyond.yaml missing restriction %q", want)
		}
	}
}

func TestDeploymentUsesRestrictedSecurityContexts(t *testing.T) {
	t.Parallel()

	manifest := readFile(t, "deployment.yaml")
	for _, want := range []string{
		"automountServiceAccountToken: false",
		"runAsNonRoot: true",
		"runAsUser: 10001",
		"runAsGroup: 10001",
		"type: RuntimeDefault",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		"drop:\n                - ALL",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("deployment.yaml missing security setting %q", want)
		}
	}
}

func TestImageUsesNumericNonRootUser(t *testing.T) {
	t.Parallel()

	dockerfile := readFile(t, "../Dockerfile")
	if want := "USER 10001:10001"; !strings.Contains(dockerfile, want) {
		t.Errorf("Dockerfile missing %q", want)
	}
}

func TestDeploymentWiresHumanAuthentication(t *testing.T) {
	t.Parallel()

	deployment := readFile(t, "deployment.yaml")
	for _, want := range []string{
		"name: PCR_OAUTH_CLIENT_ID",
		"key: oauth-client-id",
		"name: PCR_OAUTH_CLIENT_SECRET",
		"key: oauth-client-secret",
	} {
		if !strings.Contains(deployment, want) {
			t.Errorf("deployment.yaml missing human auth setting %q", want)
		}
	}

	configMap := readFile(t, "configmap.yaml")
	for _, want := range []string{
		`PCR_HUMAN_AUTH_PROVIDER: "github"`,
		`PCR_PUBLIC_URL: "https://changes.example.com"`,
		`PCR_OIDC_ISSUER_URL: ""`,
		`PCR_ALLOWED_ORGS: "example-inc"`,
		`PCR_HUMAN_SESSION_DURATION: "12h"`,
	} {
		if !strings.Contains(configMap, want) {
			t.Errorf("configmap.yaml missing human auth setting %q", want)
		}
	}

	secret := readFile(t, "secret.yaml")
	for _, want := range []string{"oauth-client-id:", "oauth-client-secret:"} {
		if !strings.Contains(secret, want) {
			t.Errorf("secret.yaml missing human auth setting %q", want)
		}
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()

	contents, err := os.ReadFile(name) //nolint:gosec // Test names are fixed repository fixtures.
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(contents)
}
