package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// OpenCSRStore opens (creating if needed) the CSR index database.
func OpenCSRStore(ctx context.Context, cfg config.CSRConfig) (*csr.Store, error) {
	if dir := filepath.Dir(cfg.DBPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("bootstrap: create %s: %w", dir, err)
		}
	}
	return csr.OpenStore(ctx, cfg.DBPath, nil)
}

// LoadInstitutionProfile reads the institution profile the CSR index scores
// companies against.
func LoadInstitutionProfile(cfg config.CSRConfig) (csr.InstitutionProfile, error) {
	f, err := os.Open(cfg.InstitutionProfilePath)
	if err != nil {
		return csr.InstitutionProfile{}, fmt.Errorf("bootstrap: open institution profile (CSR_INSTITUTION_PROFILE): %w", err)
	}
	defer f.Close()
	return csr.LoadInstitutionProfile(f)
}
