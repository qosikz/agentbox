package k8s

import (
	"strings"
	"testing"
)

// validSpec returns a JobSpec that passes validation: secure defaults plus the
// four fields a caller must always supply.
func validSpec() JobSpec {
	s := DefaultJobSpec()
	s.Name = "fix-tests"
	s.Namespace = "andbo-runs"
	s.Image = "ghcr.io/qosikz/andbo/runtime:latest"
	s.Command = []string{"andbo-agent"}
	return s
}

func TestDefaultJobSpec_IsSecure(t *testing.T) {
	s := DefaultJobSpec()

	if s.NetworkMode != NetworkDeny {
		t.Errorf("default NetworkMode = %q, want %q", s.NetworkMode, NetworkDeny)
	}
	if s.RunAsUser == 0 {
		t.Error("default RunAsUser must not be 0 (root)")
	}
	if s.ServiceAccountName != "" {
		t.Errorf("default ServiceAccountName = %q, want empty (opt-in only)", s.ServiceAccountName)
	}
	if s.RuntimeClassName != "" {
		t.Errorf("default RuntimeClassName = %q, want empty (opt-in only)", s.RuntimeClassName)
	}
	if s.ActiveDeadlineSeconds <= 0 || s.ActiveDeadlineSeconds > MaxActiveDeadlineSeconds {
		t.Errorf("default ActiveDeadlineSeconds = %d, want within (0, %d]", s.ActiveDeadlineSeconds, MaxActiveDeadlineSeconds)
	}
	if s.TTLSecondsAfterFinished <= 0 || s.TTLSecondsAfterFinished > MaxTTLSecondsAfterFinished {
		t.Errorf("default TTLSecondsAfterFinished = %d, want within (0, %d]", s.TTLSecondsAfterFinished, MaxTTLSecondsAfterFinished)
	}
	for name, v := range map[string]string{
		"CPURequest":    s.CPURequest,
		"CPULimit":      s.CPULimit,
		"MemoryRequest": s.MemoryRequest,
		"MemoryLimit":   s.MemoryLimit,
	} {
		if v == "" {
			t.Errorf("default %s must be bounded, got empty", name)
		}
	}
}

func TestValidate_AcceptsSecureSpec(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("validSpec().Validate() = %v, want nil", err)
	}
}

func TestValidate_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*JobSpec)
		wantSub string // substring the actionable error must contain
	}{
		{
			name:    "empty name",
			mutate:  func(s *JobSpec) { s.Name = "" },
			wantSub: "name",
		},
		{
			name:    "name is not a DNS-1123 label",
			mutate:  func(s *JobSpec) { s.Name = "Fix_Tests" },
			wantSub: "DNS-1123",
		},
		{
			name:    "name too long for a generated pod name",
			mutate:  func(s *JobSpec) { s.Name = strings.Repeat("a", MaxNameLength+1) },
			wantSub: "characters",
		},
		{
			name:    "name with yaml injection attempt",
			mutate:  func(s *JobSpec) { s.Name = "job\nhostNetwork: true" },
			wantSub: "DNS-1123",
		},
		{
			name:    "empty namespace",
			mutate:  func(s *JobSpec) { s.Namespace = "" },
			wantSub: "namespace",
		},
		{
			name:    "kube-system namespace",
			mutate:  func(s *JobSpec) { s.Namespace = "kube-system" },
			wantSub: "reserved",
		},
		{
			name:    "kube-public namespace",
			mutate:  func(s *JobSpec) { s.Namespace = "kube-public" },
			wantSub: "reserved",
		},
		{
			name:    "openshift-prefixed namespace",
			mutate:  func(s *JobSpec) { s.Namespace = "openshift-monitoring" },
			wantSub: "reserved",
		},
		{
			name:    "privileged add-on namespace",
			mutate:  func(s *JobSpec) { s.Namespace = "cert-manager" },
			wantSub: "privilege",
		},
		{
			name:    "empty image",
			mutate:  func(s *JobSpec) { s.Image = "" },
			wantSub: "image",
		},
		{
			name:    "image with whitespace",
			mutate:  func(s *JobSpec) { s.Image = "repo/img:tag extra" },
			wantSub: "image",
		},
		{
			name:    "empty command",
			mutate:  func(s *JobSpec) { s.Command = nil },
			wantSub: "command",
		},
		{
			name:    "command with empty element",
			mutate:  func(s *JobSpec) { s.Command = []string{""} },
			wantSub: "command",
		},
		{
			name:    "root user rejected",
			mutate:  func(s *JobSpec) { s.RunAsUser = 0 },
			wantSub: "non-root",
		},
		{
			name:    "negative user rejected",
			mutate:  func(s *JobSpec) { s.RunAsUser = -1 },
			wantSub: "non-root",
		},
		{
			name:    "network mode empty is ambiguous",
			mutate:  func(s *JobSpec) { s.NetworkMode = "" },
			wantSub: "network mode",
		},
		{
			name:    "network allowlist is not implemented",
			mutate:  func(s *JobSpec) { s.NetworkMode = "allowlist" },
			wantSub: "not implemented",
		},
		{
			name:    "network open is not supported",
			mutate:  func(s *JobSpec) { s.NetworkMode = "open" },
			wantSub: "not implemented",
		},
		{
			name:    "unknown network mode",
			mutate:  func(s *JobSpec) { s.NetworkMode = "bridge" },
			wantSub: "network mode",
		},
		{
			name:    "deadline zero",
			mutate:  func(s *JobSpec) { s.ActiveDeadlineSeconds = 0 },
			wantSub: "activeDeadlineSeconds",
		},
		{
			name:    "deadline over cap",
			mutate:  func(s *JobSpec) { s.ActiveDeadlineSeconds = MaxActiveDeadlineSeconds + 1 },
			wantSub: "activeDeadlineSeconds",
		},
		{
			name:    "ttl zero",
			mutate:  func(s *JobSpec) { s.TTLSecondsAfterFinished = 0 },
			wantSub: "ttlSecondsAfterFinished",
		},
		{
			name:    "ttl over cap",
			mutate:  func(s *JobSpec) { s.TTLSecondsAfterFinished = MaxTTLSecondsAfterFinished + 1 },
			wantSub: "ttlSecondsAfterFinished",
		},
		{
			name:    "cpu limit missing",
			mutate:  func(s *JobSpec) { s.CPULimit = "" },
			wantSub: "cpu",
		},
		{
			name:    "memory limit missing",
			mutate:  func(s *JobSpec) { s.MemoryLimit = "" },
			wantSub: "memory",
		},
		{
			name:    "cpu request above limit",
			mutate:  func(s *JobSpec) { s.CPURequest = "4"; s.CPULimit = "1" },
			wantSub: "cpu",
		},
		{
			name:    "memory request above limit",
			mutate:  func(s *JobSpec) { s.MemoryRequest = "4Gi"; s.MemoryLimit = "512Mi" },
			wantSub: "memory",
		},
		{
			name:    "malformed cpu quantity",
			mutate:  func(s *JobSpec) { s.CPULimit = "one" },
			wantSub: "cpu",
		},
		{
			name:    "workspace size limit missing",
			mutate:  func(s *JobSpec) { s.WorkspaceSizeLimit = "" },
			wantSub: "workspace",
		},
		{
			name:    "tmp size limit missing",
			mutate:  func(s *JobSpec) { s.TmpSizeLimit = "" },
			wantSub: "tmp",
		},
		{
			name:    "workdir must be absolute",
			mutate:  func(s *JobSpec) { s.WorkingDir = "work" },
			wantSub: "absolute",
		},
		{
			name:    "invalid env name",
			mutate:  func(s *JobSpec) { s.Env = map[string]string{"not-valid": "x"} },
			wantSub: "env",
		},
		{
			name:    "control character in env value",
			mutate:  func(s *JobSpec) { s.Env = map[string]string{"OK": "a\nprivileged: true"} },
			wantSub: "env",
		},
		{
			name:    "invalid service account name",
			mutate:  func(s *JobSpec) { s.ServiceAccountName = "Bad SA" },
			wantSub: "serviceAccountName",
		},
		{
			name:    "invalid runtime class name",
			mutate:  func(s *JobSpec) { s.RuntimeClassName = "Bad Class" },
			wantSub: "runtimeClassName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mutate(&s)

			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantSub)) {
				t.Errorf("Validate() error = %q, want it to mention %q", err.Error(), tt.wantSub)
			}

			// A spec that fails validation must never render.
			if out, rerr := s.Render(); rerr == nil {
				t.Errorf("Render() succeeded for an invalid spec; output:\n%s", out)
			}
		})
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	s := validSpec()
	s.Name = ""
	s.Image = ""
	s.NetworkMode = "open"

	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	msg := err.Error()
	for _, want := range []string{"name", "image", "network"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("aggregated error %q is missing %q", msg, want)
		}
	}
}

func TestValidate_AllowsOptionalHardeningFields(t *testing.T) {
	s := validSpec()
	s.RuntimeClassName = "gvisor"
	s.ServiceAccountName = "andbo-agent"
	s.Env = map[string]string{"ANDBO_TASK": "fix failing tests", "CI": "true"}
	s.Args = []string{"--task", "fix failing tests"}

	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestEnforcementNotes_AreHonest(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	// Honesty: NetworkPolicy is inert without a CNI that implements it.
	if !strings.Contains(notes, "cni") {
		t.Errorf("enforcement notes must state the CNI dependency, got:\n%s", notes)
	}
	// Honesty: this package renders only; it never applies anything.
	if !strings.Contains(notes, "apply") {
		t.Errorf("enforcement notes must state that manifests are not applied, got:\n%s", notes)
	}
	// Honesty: no domain allowlist enforcement exists here.
	if !strings.Contains(notes, "allowlist") {
		t.Errorf("enforcement notes must state that domain allowlisting is unavailable, got:\n%s", notes)
	}
}
