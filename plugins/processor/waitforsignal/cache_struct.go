package signalsampler

import (
	"errors"
	"fmt"

	"github.com/alibaba/ilogtail/pkg/protocol"
)

/*
* RingCacheBuffer 用于缓存输入的数据,等待合适的时候后,如果没有接收到该数据的处理信号,就进行丢弃

通常情况下,调用者不需要关心组件到底缓存了多久,只需要通过API提示组件开始处理旧数据,默认的数据规则如下

1. 以10s为一个时间片(fragment),所有累积经过6个时间片的数据被丢弃

2. 整个系统累计的日志超过500MB ~= 后从最早的数据开始丢弃

3. 整个系统累计的日志超过3e5条之后开始丢弃
*
*/
type RingCacheBuffer struct {
	// StartIndex 数据起始索引,随规则1/3/4更新,需要保证线程安全
	StartIndex int
	// 数据终止索引,随数据插入更新,需要保证线程安全
	// StartIndex < EndIndex 时, 有效数据区为 [StartIndex,EndIndex]
	// StartIndex > EndIndex 时, 有效数据区为 [0,EndIndex],[StartIndex,MaxSize],
	EndIndex int

	// 总存储数据量大小,随数据插入或修改更新
	GlobalDataSize int

	// 超长Slice，用于环形缓存接收的日志数据
	// 所有进入缓存的数据在有效数据区内按采样时间升序
	Cache []*CacheLog

	// 最大缓存的日志条数
	MaxSize int
	// 最大缓存的日志体积
	MaxCacheByte int
	// 用于记录缓存数据过期的时间和未过期数据的开始Index
	OutDateIndex *OutDateIndex
}

func NewRingCacheBuffer(maxSize int, maxCacheByte int) *RingCacheBuffer {
	return &RingCacheBuffer{
		Cache:          make([]*CacheLog, maxSize),
		MaxSize:        maxSize,
		MaxCacheByte:   maxCacheByte,
		StartIndex:     0,
		EndIndex:       0,
		GlobalDataSize: 0,
		OutDateIndex: &OutDateIndex{
			NextStartIndexIdx: 0,
			start:             false,
		},
	}
}

func (b *RingCacheBuffer) AddIntoBuffer(log *CacheLog) {
	if (log.Size() + b.GlobalDataSize) > int(b.MaxCacheByte) {
		// 计算需要从最前丢弃多少个事件
		b.GlobalDataSize -= b.Cache[b.StartIndex].Size()
		// 由于数据过多丢弃数据,通过置空的方式等待GC回收
		b.Cache[b.StartIndex] = nil
		b.StartIndex = (b.StartIndex + 1) % b.MaxSize
	}

	b.GlobalDataSize += log.Size()
	b.Cache[b.EndIndex] = log
	b.EndIndex = (b.EndIndex + 1) % b.MaxSize
}

func (b *RingCacheBuffer) searchRange(signal *LogSignalStatus, samplerIndex int, res []*CacheLog, from int, to int) []*CacheLog {
	// 倒序遍历
	for i := to - 1; i >= from; i-- {
		if len(res) > 2000 {
			break
		}

		logEntry := b.Cache[i]
		// 跳过空日志和其他采集器的日志
		if logEntry == nil || logEntry.SourceKeyRef.FromSampler != samplerIndex {
			continue
		}
		// 如果日志时间过早, 不再向前遍历
		if logEntry.LogCapturedTime < uint32(signal.StartTS)-2 {
			break
		}
		// if logEntry.CacheTimestamp < uint32(signal.StartTS)-1 || logEntry.CacheTimestamp > uint32(signal.EndTS)+1 || logEntry.Exposed {
		// 	continue
		// }
		if !signal.IsMatched(logEntry) {
			continue
		}
		if logEntry.IsExposed {
			// 不重复输出已经导出的日志
			// 晚于IsMatched判断来更新Signal的MatchedLog状态
			continue
		}
		logEntry.IsExposed = true
		logEntry.SpanId = signal.ApmSpanId
		res = append(res, logEntry)
	}
	return res
}

func (b *RingCacheBuffer) GetStartTS() uint32 {
	if b == nil || b.Cache == nil {
		return 0
	}
	startItem := b.Cache[b.StartIndex]
	if startItem == nil {
		return 0
	}
	return startItem.LogCapturedTime
}

func (b *RingCacheBuffer) GetEndTS() uint32 {
	if b == nil || b.Cache == nil {
		return 0
	}

	var endItem *CacheLog
	if b.EndIndex < 1 {
		endItem = b.Cache[b.MaxSize-1]
	} else {
		endItem = b.Cache[b.EndIndex-1]
	}

	if endItem == nil {
		return 0
	}

	return endItem.LogCapturedTime
}

func (b *RingCacheBuffer) CheckAndUpdateOutDateIndex() (updated bool, err error) {
	var lastLogEntryIndex int
	if b.EndIndex > 0 {
		lastLogEntryIndex = b.EndIndex - 1
	} else {
		lastLogEntryIndex = b.MaxSize - 1
	}

	var lastLogEntry = b.Cache[lastLogEntryIndex]
	if lastLogEntry != nil {
		defer b.OutDateIndex.UpdateDateCache(lastLogEntryIndex, lastLogEntry.LogCapturedTime)
	}

	nextStartIndex, nextStartTS := b.OutDateIndex.NextStartIndex()
	if nextStartTS == 0 {
		return false, errors.New("outDate Cache still initialing")
	}

	// 旧的Index已经落在无效区,已经因为其他原因被清理
	if b.EndIndex <= nextStartIndex && nextStartIndex < b.StartIndex {
		return false, fmt.Errorf("lastIndex is invalid, startIdx: %d, endIdx: %d, nextStartIdx: %d", b.StartIndex, b.EndIndex, nextStartIndex)
	}
	if b.StartIndex <= b.EndIndex && (nextStartIndex < b.StartIndex || nextStartIndex > b.EndIndex) {
		return false, fmt.Errorf("lastIndex is invalid, startIdx: %d, endIdx: %d, nextStartIdx: %d", b.StartIndex, b.EndIndex, nextStartIndex)
	}

	// 当前Index的事件的TS和缓存不一致,数据已经被覆盖,无需清理
	if b.Cache[nextStartIndex].LogCapturedTime != nextStartTS {
		return false, fmt.Errorf("nextStartIdx is invalid, nextStartIdx:%d, cacheTS:%d, realTS:%d", nextStartIndex, nextStartTS, b.Cache[nextStartIndex].LogCapturedTime)
	}

	b.StartIndex = nextStartIndex
	return true, nil
}

type CacheLog struct {
	*protocol.Log

	SourceKeyRef SourceKey
	// LogCapturedTime 日志采集时间
	LogCapturedTime uint32
	// 日志导出时关联的信号的spanId
	// 一个日志可能匹配多个信号, 只存储第一个匹配的信号
	SpanId string

	// GlobalIndex 所有日志共用一套Index,Index将支持索引所有的日志
	GlobalIndex

	// IsExposed 日志已经被输出的标志
	IsExposed bool
}

type GlobalIndex struct {
	LogIndexInProcess uint
}

type SourceKey struct {
	// 数据来源的插件，目前包括 stdout 和 input_file 两种
	FromSampler int    `json:"-"`
	ContainerId string `json:"containerId"`
	ProcessPid  int    `json:"processPid"`

	Namespace     string `json:"namespace"`
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName"`
}

// OutDateIndex 用于记录缓存数据过期的时间
// 每10s进行一次过期检查
// 每次检查时，将60s前的数据标记为过期
type OutDateIndex struct {
	StartIndexCache [6]int
	EventTimestamp  [6]uint32

	// range from 0-5
	NextStartIndexIdx int

	start bool
}

// 提供未过期数据的开始Index
func (oi *OutDateIndex) NextStartIndex() (index int, ts uint32) {
	if !oi.start {
		return 0, 0
	}
	return oi.StartIndexCache[oi.NextStartIndexIdx], oi.EventTimestamp[oi.NextStartIndexIdx]
}

// 更新缓存的数据过期状态
func (oi *OutDateIndex) UpdateDateCache(index int, ts uint32) {
	// 首轮缓存结束后
	if oi.NextStartIndexIdx >= 5 {
		oi.start = true
	}

	oi.NextStartIndexIdx = (oi.NextStartIndexIdx + 1) % 6
	newCacheIdx := (oi.NextStartIndexIdx + 5) % 6
	oi.StartIndexCache[newCacheIdx] = index
	oi.EventTimestamp[newCacheIdx] = ts
}
