package signalsampler

import (
	"strconv"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/protocol"
)

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
		s.cacheLogV1(log, containerId, pid)
	}
	return s.GetExposedLogV1()
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

		switch cont.Key {
		case "container_id":
			if len(cont.Value) > 12 {
				cont.Value = cont.Value[0:12]
			}
			containerId = cont.Value
		case "pid":
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

func (s *SignalSampler) cacheLogV1(log *protocol.Log, containerId string, pid int) {
	cachedLog := CacheLog{
		LogRef: LogRef{
			v1Log: log,
		},
		SourceKeyRef: SourceKey{
			FromSampler: s.SamplerId,
			ContainerId: containerId,
			ProcessPid:  pid,
		},
		LogCapturedTime: uint64(log.Time),
		GlobalIndex: GlobalIndex{
			// ProcessIndex: 0,
			LogIndexInProcess: s.logCounter,
		},
	}

	s.logChan <- &cachedLog
}

func (s *SignalSampler) addExposedV1Log(outLog *CacheLog) {
	if s.logEventExposed == nil {
		s.logEventExposed = make([]*protocol.Log, 0, 8)
	}

	v1Log := outLog.GetV1Log()

	// outLog.Log.Contents = append(outLog.Log.Contents, &protocol.Log_Content{Key: "span_id", Value: outLog.SpanId})
	v1Log.Contents = append(v1Log.Contents, &protocol.Log_Content{Key: "_time_ns_", Value: strconv.FormatUint(outLog.GetTimeNs(), 10)})
	v1Log.Contents = append(v1Log.Contents, &protocol.Log_Content{Key: "log_seq", Value: strconv.FormatUint(uint64(outLog.LogIndexInProcess), 10)})

	s.logEventExposed = append(s.logEventExposed, outLog.GetV1Log())
}

func (s *SignalSampler) GetExposedLogV1() []*protocol.Log {
	if len(s.logEventExposed) == 0 {
		return nil
	}

	s.exposedMutex.Lock()
	defer s.exposedMutex.Unlock()
	out := s.logEventExposed
	s.logEventExposed = nil
	return out
}
