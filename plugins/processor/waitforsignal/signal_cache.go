package signalsampler

import (
	"sort"
	"time"
)

type SignalList struct {
	// 所有信号按采集结束时间升序排序
	cachedSignalList []*LogSignalStatus
}

type byEndTs []*LogSignalStatus

func (b byEndTs) Len() int { return len(b) }

func (b byEndTs) Less(i, j int) bool {
	// 先EndTime升序，再startTime升序
	if b[i].EndTS == b[j].EndTS {
		return b[i].StartTS < b[j].StartTS
	}
	return b[i].EndTS < b[j].EndTS
}
func (b byEndTs) Swap(i, j int) { b[i], b[j] = b[j], b[i] }

type LogSignalStatus struct {
	Signal

	// 从Signal中取得的Pid是字符串形式的
	// 后续直接用int格式Pid
	Pid int

	MatchedLog bool
}

func (s *LogSignalStatus) IsMatched(cachedLog *CacheLog) bool {
	// 判断时间是否匹配
	if s.StartTS-1 > cachedLog.LogCapturedTime || s.EndTS+1 < cachedLog.LogCapturedTime {
		return false
	}
	if len(cachedLog.SourceKeyRef.ContainerId) > 0 && cachedLog.SourceKeyRef.ContainerId != s.ContainerId {
		return false
	}
	if cachedLog.SourceKeyRef.ProcessPid > 0 && cachedLog.SourceKeyRef.ProcessPid != s.Pid {
		return false
	}
	// 只要有任意一条匹配，都进行打标为TRUE
	s.MatchedLog = true
	return true
}

func NewSignalList() *SignalList {
	return &SignalList{
		cachedSignalList: make([]*LogSignalStatus, 0),
	}
}

func (c *SignalList) AddSignal(signal *LogSignalStatus) {
	// 新加入数据EndTime < 最后一条数据的EndTime，需进行排序
	shouldSort := len(c.cachedSignalList) > 0 && c.cachedSignalList[len(c.cachedSignalList)-1].EndTS > signal.EndTS
	c.cachedSignalList = append(c.cachedSignalList, signal)
	if shouldSort {
		sort.Sort(byEndTs(c.cachedSignalList))
	}
}

func (c *SignalList) RangeMatch(cachedLog *CacheLog) bool {
	var matched bool
	for i := len(c.cachedSignalList) - 1; i >= 0; i-- {
		signal := c.cachedSignalList[i]
		if signal != nil {
			if signal.IsMatched(cachedLog) {
				matched = true
			} else if signal.EndTS+1 < cachedLog.LogCapturedTime {
				// 此处已EndTime进行排序，超出时间范围, 后续信号不可能匹配到,直接返回
				return matched
			}
		}
	}
	return matched
}

func (c *SignalList) CleanExpiredSignals(isExpiredNeed bool) []*LogSignalStatus {
	var expiredSignalList = make([]*LogSignalStatus, 0)
	now := uint32(time.Now().Unix())

	for i := 0; i < len(c.cachedSignalList); i++ {
		signal := c.cachedSignalList[i]
		if signal.EndTS+2 < now {
			// 信号多保留2秒，确保日志不及时到大能进行容错.
			if isExpiredNeed {
				expiredSignalList = append(expiredSignalList, signal)
			}
		} else {
			notExpiredSignals := c.cachedSignalList[i:]
			newCacheList := make([]*LogSignalStatus, len(notExpiredSignals))
			copy(newCacheList, notExpiredSignals)
			c.cachedSignalList = newCacheList
			return expiredSignalList
		}
	}
	c.cachedSignalList = make([]*LogSignalStatus, 0)
	return expiredSignalList
}
