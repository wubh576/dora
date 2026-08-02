package attention

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionLabel 只生成日志关联标识，不暴露可用于回跳的原始 session ID。
func SessionLabel(externalSessionID string) string {
	hash := sha256.Sum256([]byte(externalSessionID))
	return hex.EncodeToString(hash[:6])
}
