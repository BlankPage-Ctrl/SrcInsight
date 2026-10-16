package cold

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Bulk multi-row chunks are sized from a variable budget instead of the
// legacy 999-variable ceiling; modern drivers allow tens of thousands.
// The budget halves itself if the driver ever reports a variable-limit error.
var bulkVarBudget atomic.Int32

var initBulkBudget = sync.OnceValue(func() int32 {
	if raw := strings.TrimSpace(os.Getenv("SRCINSIGHT_BULK_VARS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return int32(n)
		}
	}
	return 2000
})

func chunkRows(cols int) int {
	b := bulkVarBudget.Load()
	if b == 0 {
		b = initBulkBudget()
		bulkVarBudget.Store(b)
	}
	n := int(b) / cols
	if n < 1 {
		return 1
	}
	return n
}

func isVarLimitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "too many SQL variables")
}

// shrinkVarBudget halves the budget for retry; false when already minimal.
func shrinkVarBudget() bool {
	for {
		b := bulkVarBudget.Load()
		if b <= 999 {
			return false
		}
		if bulkVarBudget.CompareAndSwap(b, b/2) {
			return true
		}
	}
}
