package raft

// StableStore is used to provide stable storage
// of key configurations to ensure safety.
// K-V持久化存储接口，可以根据需要具体实现
// Uint64存储 keyCurrentTerm、keyLastVoteTerm
// Set存储 keyLastVoteCand
type StableStore interface {
	Set(key []byte, val []byte) error

	// Get returns the value for key, or an empty byte slice if key was not found.
	Get(key []byte) ([]byte, error)

	SetUint64(key []byte, val uint64) error

	// GetUint64 returns the uint64 value for key, or 0 if key was not found.
	GetUint64(key []byte) (uint64, error)
}
