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
		"startupProbe:\n            httpGet:\n              path: /readyz",
		"livenessProbe:\n            httpGet:\n              path: /livez",
		"readinessProbe:\n            httpGet:\n              path: /readyz",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("deployment.yaml missing health probe:\n%s", want)
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

func readFile(t *testing.T, name string) string {
	t.Helper()

	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(contents)
}
