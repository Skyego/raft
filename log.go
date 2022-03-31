package raft

import (
	"fmt"
	"time"

	metrics "github.com/armon/go-metrics"
)

// LogType describes various types of log entries.
type LogType uint8

const (
	// LogCommand is applied to a user FSM.
	LogCommand LogType = iota

	// LogNoop is used to assert leadership.
	LogNoop

	// LogAddPeerDeprecated is used to add a new peer. This should only be used with
	// older protocol versions designed to be compatible with unversioned
	// Raft servers. See comments in config.go for details.
	// 下面是两个旧版peer成员变更的方式，也需要看一下怎么操作的
	LogAddPeerDeprecated

	// LogRemovePeerDeprecated is used to remove an existing peer. This should only be
	// used with older protocol versions designed to be compatible with
	// unversioned Raft servers. See comments in config.go for details.
	LogRemovePeerDeprecated

	// LogBarrier is used to ensure all preceding operations have been
	// applied to the FSM. It is similar to LogNoop, but instead of returning
	// once committed, it only returns once the FSM manager acks it. Otherwise
	// it is possible there are operations committed but not yet applied to
	// the FSM.
	// 用于确保所有前面的操作都已应用于FSM。
	//它类似于LogNoop，但不是在提交后返回，它仅在FSM 管理器确认后返回。
	//否则，可能存在已提交但尚未应用于FSM 的操作。
	LogBarrier

	// LogConfiguration establishes a membership change configuration. It is
	// created when a server is added, removed, promoted, etc. Only used
	// when protocol version 1 or greater is in use.
	LogConfiguration
)

// String returns LogType as a human readable string.
func (lt LogType) String() string {
	switch lt {
	case LogCommand:
		return "LogCommand"
	case LogNoop:
		return "LogNoop"
	case LogAddPeerDeprecated:
		return "LogAddPeerDeprecated"
	case LogRemovePeerDeprecated:
		return "LogRemovePeerDeprecated"
	case LogBarrier:
		return "LogBarrier"
	case LogConfiguration:
		return "LogConfiguration"
	default:
		return fmt.Sprintf("%d", lt)
	}
}

// Log entries are replicated to all members of the Raft cluster
// and form the heart of the replicated state machine.
type Log struct {
	// Index索引值 holds the index of the log entry.
	Index uint64

	// Term任期编号 holds the election term of the log entry.
	Term uint64

	// Type日志项类别 holds the type of the log entry.
	Type LogType

	// Data指令 holds the log entry's type-specific data.
	Data []byte

	// Extensions holds an opaque byte slice of information for middleware. It
	// is up to the client of the library to properly modify this as it adds
	// layers and remove those layers when appropriate. This value is a part of
	// the log, so very large values could cause timing issues.
	// 扩展信息
	// N.B. It is _up to the client_ to handle upgrade paths. For instance if
	// using this with go-raftchunking, the client should ensure that all Raft
	// peers are using a version that can handle that extension before ever
	// actually triggering chunking behavior. It is sometimes sufficient to
	// ensure that non-leaders are upgraded first, then the current leader is
	// upgraded, but a leader changeover during this process could lead to
	// trouble, so gating extension behavior via some flag in the client
	// program is also a good idea.
	// 扩展为中间件保存一个不透明的字节信息片。
	//由库的客户端在添加层并在适当时删除这些层时正确修改它。
	//此值是日志的一部分，因此非常大的值可能会导致时间问题。
	//
	//注： _由客户端_来处理升级路径。
	//例如，如果将其与 go-raftchunking 一起使用，客户端应确保所有 RaftPeers都使用可以处理该扩展的版本，然后再实际触发分块行为。
	//有时确保先升级非领导者，然后升级当前领导者就足够了，但在此过程中领导者切换可能会导致麻烦，因此通过客户端程序中的某些标志来控制扩展行为也是一个好主意。
	Extensions []byte

	// AppendedAt stores the time the leader first appended this log to it's
	// LogStore. Followers will observe the leader's time. It is not used for
	// coordination or as part of the replication protocol at all. It exists only
	// to provide operational information for example how many seconds worth of
	// logs are present on the leader which might impact follower's ability to
	// catch up after restoring a large snapshot. We should never rely on this
	// being in the past when appending on a follower or reading a log back since
	// the clock skew can mean a follower could see a log with a future timestamp.
	// In general too the leader is not required to persist the log before
	// delivering to followers although the current implementation happens to do
	// this.
	// 存储领导者首次将此日志附加到其 LogStore 的时间。
	//追随者将观察领导者的时间。它根本不用于协调或作为复制协议的一部分。
	//它的存在只是为了提供操作信息，例如领导者上存在多少秒的日志，这可能会影响跟随者在恢复大快照后赶上的能力。
	//在附加到追随者或读回日志时，我们永远不应该依赖于过去，因为时钟偏差可能意味着追随者可以看到带有未来时间戳的日志。
	//一般来说，领导者也不需要在交付给追随者之前保留日志，尽管当前的实现恰好这样做。
	AppendedAt time.Time
}

// LogStore is used to provide an interface for storing
// and retrieving logs in a durable fashion.
type LogStore interface {
	// FirstIndex returns the first index written. 0 for no entries.
	FirstIndex() (uint64, error)

	// LastIndex returns the last index written. 0 for no entries.
	LastIndex() (uint64, error)

	// GetLog gets a log entry at a given index.
	GetLog(index uint64, log *Log) error

	// StoreLog stores a log entry.
	StoreLog(log *Log) error

	// StoreLogs stores multiple log entries.
	StoreLogs(logs []*Log) error

	// DeleteRange deletes a range of log entries. The range is inclusive.
	DeleteRange(min, max uint64) error
}

func oldestLog(s LogStore) (Log, error) {
	var l Log

	// We might get unlucky and have a truncate right between getting first log
	// index and fetching it so keep trying until we succeed or hard fail.
	var lastFailIdx uint64
	var lastErr error
	for {
		firstIdx, err := s.FirstIndex()
		if err != nil {
			return l, err
		}
		if firstIdx == 0 {
			return l, ErrLogNotFound
		}
		if firstIdx == lastFailIdx {
			// Got same index as last time around which errored, don't bother trying
			// to fetch it again just return the error.
			return l, lastErr
		}
		err = s.GetLog(firstIdx, &l)
		if err == nil {
			// We found the oldest log, break the loop
			break
		}
		// We failed, keep trying to see if there is a new firstIndex
		lastFailIdx = firstIdx
		lastErr = err
	}
	return l, nil
}

func emitLogStoreMetrics(s LogStore, prefix []string, interval time.Duration, stopCh <-chan struct{}) {
	for {
		select {
		case <-time.After(interval):
			// In error case emit 0 as the age
			ageMs := float32(0.0)
			l, err := oldestLog(s)
			if err == nil && !l.AppendedAt.IsZero() {
				ageMs = float32(time.Since(l.AppendedAt).Milliseconds())
			}
			metrics.SetGauge(append(prefix, "oldestLogAge"), ageMs)
		case <-stopCh:
			return
		}
	}
}
