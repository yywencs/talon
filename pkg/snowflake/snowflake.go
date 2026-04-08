package snowflake

import (
	"sync"
	"time"
)

// 雪花算法实现
type snowflakeGenerator struct {
	mutex         sync.Mutex
	lastTimestamp int64
	sequence      int64
	workerID      int64
	datacenterID  int64
}

const (
	workerIDBits      = 5
	datacenterIDBits  = 5
	sequenceBits      = 12
	maxWorkerID       = -1 ^ (-1 << workerIDBits)
	maxDatacenterID   = -1 ^ (-1 << datacenterIDBits)
	sequenceMask      = -1 ^ (-1 << sequenceBits)
	workerIDShift     = sequenceBits
	datacenterIDShift = sequenceBits + workerIDBits
	timestampShift    = sequenceBits + workerIDBits + datacenterIDBits
)

var snowflakeGen = &snowflakeGenerator{
	workerID:     1,
	datacenterID: 1,
}

func (s *snowflakeGenerator) generate() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	now := time.Now().UnixMilli()
	if s.lastTimestamp == now {
		s.sequence = (s.sequence + 1) & sequenceMask
		if s.sequence == 0 {
			for now <= s.lastTimestamp {
				now = time.Now().UnixMilli()
			}
		}
	} else {
		s.sequence = 0
	}

	s.lastTimestamp = now
	return (now << timestampShift) |
		(s.datacenterID << datacenterIDShift) |
		(s.workerID << workerIDShift) |
		s.sequence
}

func GenerateEventID() int64 {
	return snowflakeGen.generate()
}
