package signalsampler

import (
	"strconv"
	"sync"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/alibaba/ilogtail/pkg/protocol"
)

const pluginName = "processor_wait_for_signal"
const MAX_CACHE_MEM_BYTE = 300 * 1024 * 1024
const MAX_CACHE_ITEM_SIZE = 3e5

// 注册插件
func init() {
	pipeline.Processors[pluginName] = func() pipeline.Processor {
		return &SignalSampler{}
	}
}

/*
SignalSampler 基于信号对输入的数据进行采样
*/
type SignalSampler struct {
	// 关闭信号采样 用于测试和全量日志采集
	DisableSignalSampler bool
	SamplerId            int
	// 最大存储内存
	MaxCacheByte int

	// Move To tags
	ContentsRename map[string]string
	// 日志计数器
	logCounter uint
	logChan    chan<- *CacheLog

	// 下次要导出的日志
	logEventExposed []*protocol.Log
	// 接收信号时要准备下次导出数据和每次处理输入后，带走导出数据冲突
	exposedMutex sync.RWMutex
	// 初始化失败的标记，如果初始化失败，不在做存储
	initWithError bool
	context       pipeline.Context
}

func (s *SignalSampler) Init(context pipeline.Context) error {
	s.context = context
	if !s.DisableSignalSampler {
		s.logChan = RegisterSampler(s)
	}
	logger.Info(context.GetRuntimeContext(), "init signal sampler with id", s.SamplerId)
	return nil
}

func (*SignalSampler) Description() string {
	return "cache and wait for signal"
}

func (s *SignalSampler) ProcessLogs(logArray []*protocol.Log) []*protocol.Log {
	// 不进行缓存采样
	if s.initWithError || s.DisableSignalSampler {
		// 仅根据配置重命名部分Tag
		return s.renameLabelOnly(logArray)
	}

	for _, log := range logArray {
		s.logCounter++

		// 提取采样关键信息,跳过空值日志
		containerId, pid, skip := s.formatLog(log)
		if skip {
			// 跳过空值日志
			continue
		}

		if len(containerId) == 0 && pid == 0 {
			logger.Infof(s.context.GetRuntimeContext(), "no container id or pid found in log")
			continue
		}

		// 缓存数据
		s.CacheLog(log, containerId, pid)
	}

	return s.GetExposedLog()
}

// formatLog,提取用于采样的关键信息
// hasEmpty: grpc传输不允许传输空键值对, 一旦遇到空值,不再处理
func (s *SignalSampler) formatLog(log *protocol.Log) (containerId string, pid int, skip bool) {
	for i := 0; i < len(log.Contents); i++ {
		cont := log.Contents[i]
		if cont.Key == "content" && cont.Value == "" {
			// 跳过空值日志
			return "", 0, true
		}

		if rename, find := s.ContentsRename[cont.Key]; find {
			cont.Key = rename
		}

		if cont.Key == "_container_id_" {
			if len(cont.Value) > 12 {
				cont.Value = cont.Value[0:12]
			}
			containerId = cont.Value
		} else if cont.Key == "pid" {
			pid, _ = strconv.Atoi(cont.Value)
		}
	}
	return containerId, pid, false
}

func (s *SignalSampler) renameLabelOnly(logArray []*protocol.Log) []*protocol.Log {
	for _, log := range logArray {
		s.logCounter++
		for _, cont := range log.Contents {
			if rename, find := s.ContentsRename[cont.Key]; find {
				cont.Key = rename
			}
			if cont.Key == "_container_id_" {
				if len(cont.Value) > 12 {
					cont.Value = cont.Value[0:12]
				}
			}
		}
		log.Contents = append(log.Contents, &protocol.Log_Content{Key: "_time_ns_", Value: strconv.FormatUint(uint64(log.GetTimeNs()), 10)})
		log.Contents = append(log.Contents, &protocol.Log_Content{Key: "log_seq", Value: strconv.FormatUint(uint64(s.logCounter), 10)})
	}
	return logArray
}

func (s *SignalSampler) CacheLog(log *protocol.Log, containerId string, pid int) {
	cachedLog := CacheLog{
		Log: log,
		SourceKeyRef: SourceKey{
			FromSampler: s.SamplerId,
			ContainerId: containerId,
			ProcessPid:  pid,
		},
		LogCapturedTime: log.GetTime(),
		GlobalIndex: GlobalIndex{
			// ProcessIndex: 0,
			LogIndexInProcess: s.logCounter,
		},
	}

	s.logChan <- &cachedLog
}

func (s *SignalSampler) AddExposedLog(outLog *CacheLog) {
	s.exposedMutex.Lock()
	defer s.exposedMutex.Unlock()
	s.addIntoExposedAndRichLog(outLog)
}

func (s *SignalSampler) AddExposedLogs(outLogs []*CacheLog) {
	s.exposedMutex.Lock()
	defer s.exposedMutex.Unlock()
	for _, outLog := range outLogs {
		s.addIntoExposedAndRichLog(outLog)
	}
}

func (s *SignalSampler) addIntoExposedAndRichLog(outLog *CacheLog) {
	if s.logEventExposed == nil {
		s.logEventExposed = make([]*protocol.Log, 0, 8)
	}

	// outLog.Log.Contents = append(outLog.Log.Contents, &protocol.Log_Content{Key: "span_id", Value: outLog.SpanId})
	outLog.Log.Contents = append(outLog.Log.Contents, &protocol.Log_Content{Key: "_time_ns_", Value: strconv.FormatUint(uint64(outLog.GetTimeNs()), 10)})
	outLog.Log.Contents = append(outLog.Log.Contents, &protocol.Log_Content{Key: "log_seq", Value: strconv.FormatUint(uint64(outLog.LogIndexInProcess), 10)})

	s.logEventExposed = append(s.logEventExposed, outLog.Log)
}

func (s *SignalSampler) GetExposedLog() []*protocol.Log {
	if len(s.logEventExposed) == 0 {
		return nil
	}

	s.exposedMutex.Lock()
	defer s.exposedMutex.Unlock()
	out := s.logEventExposed
	s.logEventExposed = nil
	return out
}
