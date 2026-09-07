package hot

import (
	"crypto/sha256"
	"fmt"
)

// hashKey is how a presented API key is looked up. Keeping it here rather than importing the kms
// package means the hot path does not reach key material to identify a caller.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)
}
