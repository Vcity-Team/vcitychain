package snapshot

import "testing"

func TestValidateCreateFlags(t *testing.T) {
	tests := []struct {
		name    string
		dataDir string
		out     string
		wantErr bool
	}{
		{name: "all flags set", dataDir: "/data", out: "/snap.tar.gz", wantErr: false},
		{name: "missing data dir", dataDir: "", out: "/snap.tar.gz", wantErr: true},
		{name: "missing out", dataDir: "/data", out: "", wantErr: true},
		{name: "both missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &snapshotParams{dataDir: tt.dataDir, out: tt.out}
			err := p.validateCreateFlags()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateCreateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRestoreFlags(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		dataDir  string
		wantErr  bool
	}{
		{name: "all flags set", snapshot: "/snap.tar.gz", dataDir: "/data", wantErr: false},
		{name: "missing snapshot", snapshot: "", dataDir: "/data", wantErr: true},
		{name: "missing data dir", snapshot: "/snap.tar.gz", dataDir: "", wantErr: true},
		{name: "both missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &snapshotParams{snapshot: tt.snapshot, dataDir: tt.dataDir}
			err := p.validateRestoreFlags()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRestoreFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateUnknownAction(t *testing.T) {
	err := validateAction("delete")
	if err == nil {
		t.Fatal("validateAction should reject unknown actions")
	}
}
