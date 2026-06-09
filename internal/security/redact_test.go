package security

import (
	"strings"
	"testing"
)

func TestRedactorScrubsCredentials(t *testing.T) {
	r := NewRedactor()
	in := strings.Join([]string{
		"api_key=SUPERSECRET_VALUE_123",
		"authorization=Bearer abc.def.ghi",
		"AKIAIOSFODNN7EXAMPLE",
		"this line is normal and should survive",
	}, "\n")

	out := r.Scrub(in)

	for _, leaked := range []string{"SUPERSECRET_VALUE_123", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("secret %q was not redacted:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "this line is normal and should survive") {
		t.Fatalf("redactor removed non-secret text:\n%s", out)
	}
}

func TestRedactorMasksSecretData(t *testing.T) {
	manifest := strings.Join([]string{
		"apiVersion: v1",
		"kind: Secret",
		"metadata:",
		"  name: db",
		"data:",
		"  password: cGFzc3dvcmQ=",
		"  username: YWRtaW4=",
	}, "\n")

	out := NewRedactor().Scrub(manifest)
	if strings.Contains(out, "cGFzc3dvcmQ=") || strings.Contains(out, "YWRtaW4=") {
		t.Fatalf("Secret data values were not masked:\n%s", out)
	}
}
