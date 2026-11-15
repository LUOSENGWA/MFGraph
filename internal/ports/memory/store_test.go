package memory_test

import (
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/ports/porttest"
)

// TestStoreContract runs the repository contract tests on the in-memory
// store (the MySQL store runs the same tests on MySQL 8.0 and seekdb).
func TestStoreContract(t *testing.T) {
	porttest.Run(t, func(*testing.T) porttest.Store { return memory.NewStore() })
}
