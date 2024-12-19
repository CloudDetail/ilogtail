package signalsampler

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/pkg/logger"
)

type GlobalCache struct {
	// 日志输入通道
	logChan chan *CacheLog

	// 全局缓存对象
	*RingCacheBuffer
	// 全局信号缓存
	SignalList

	samplerMaps map[int]*SignalSampler
	samplerMux  sync.RWMutex
}

var globalCache *GlobalCache
var initCacheOnce sync.Once

var configNameIdMap map[string]int = map[string]int{}

const sockAddr = "/opt/signal/logexpose.sock"

func initGlobalCache(maxCacheMemByte int) *GlobalCache {
	if maxCacheMemByte <= 0 || maxCacheMemByte > MAX_CACHE_MEM_BYTE {
		maxCacheMemByte = MAX_CACHE_MEM_BYTE
	}

	return &GlobalCache{
		logChan:         make(chan *CacheLog),
		RingCacheBuffer: NewRingCacheBuffer(MAX_CACHE_ITEM_SIZE, maxCacheMemByte),
		samplerMaps:     map[int]*SignalSampler{},
	}
}

// RegisterSampler 注册Sampler获取一个只写通道
// 目前使用流水线配置文件名作为Sampler标识
// TODO 移除流水线后，原有的Sampler未被释放，存在内存泄漏隐患
func RegisterSampler(s *SignalSampler) (logChan chan<- *CacheLog) {
	initCacheOnce.Do(func() {
		// 借用负责初始化该结构的组件的上下文
		logger.Info(s.context.GetRuntimeContext(), "prepare to start GlobalCache and wait signal at ", sockAddr)
		signalServer, err := NewUnixSocketServer(sockAddr)
		if err != nil {
			return
		}
		globalCache = initGlobalCache(s.MaxCacheByte)
		// 开始进行数据缓存和处理
		go globalCache.ProcessLogsAndSignal(signalServer.SignalChan)
		logger.Info(s.context.GetRuntimeContext(), "start GlobalCache success, wait signal at ", sockAddr)
	})

	if globalCache == nil {
		s.initWithError = true
		return
	}

	// 注册Sampler信息，在后续Signal到来时进行调用
	globalCache.samplerMux.Lock()
	defer globalCache.samplerMux.Unlock()
	if s.SamplerId == 0 {
		configName := s.context.GetConfigName()
		if id, find := configNameIdMap[configName]; find {
			s.SamplerId = id
		} else {
			newConfigId := len(configNameIdMap)
			configNameIdMap[configName] = newConfigId
			s.SamplerId = newConfigId
		}
	}
	globalCache.samplerMaps[s.SamplerId] = s

	// 使用定义的最小的MaxCacheByte
	if s.MaxCacheByte > 0 && s.MaxCacheByte < globalCache.RingCacheBuffer.MaxCacheByte {
		globalCache.RingCacheBuffer.MaxCacheByte = s.MaxCacheByte
	}

	logger.Info(s.context.GetRuntimeContext(), "register signal sampler ", s.SamplerId)
	return globalCache.logChan
}

func (g *GlobalCache) ProcessLogsAndSignal(signalChan chan Signal) {
	outDateTimer := time.NewTicker(10 * time.Second)
	defer outDateTimer.Stop()

	for {
		select {
		case <-outDateTimer.C:
			// 清理信号缓存(为过期的信号生成记录)
			expiredSignals := g.SignalList.CleanExpiredSignals(true)
			for _, signal := range expiredSignals {
				if signal.MatchedLog {
					logger.Debug(context.Background(), "clean expired signal, signal matched log", *signal)
				} else {
					logger.Debug(context.Background(), "clean expired signal, signal not matched any log", *signal)
				}
			}

			// 清理过期日志
			isUpdated, err := g.RingCacheBuffer.CheckAndUpdateOutDateIndex()
			if !isUpdated {
				logger.Debug(context.Background(), "failed to update global cache, reason: ", err.Error())
			}
		case signal := <-signalChan:
			logger.Debug(context.Background(), "received signal", signal)
			pid, err := strconv.Atoi(signal.PidStr)
			if err != nil {
				pid = 0
			}
			signalStatus := &LogSignalStatus{
				Signal:     signal,
				Pid:        pid,
				MatchedLog: false,
			}

			// 先全局扫描一遍已缓存日志,将匹配的日志存入Sampler下次导出列表
			g.GetExposedLogs(signalStatus)

			// 将信号存储到全局缓存
			g.SignalList.AddSignal(signalStatus)
		case log := <-g.logChan:
			// 遍历信号的全局缓存,如果匹配到信号,导出并标记为已导出
			if g.SignalList.RangeMatch(log) {
				g.samplerMaps[log.SourceKeyRef.FromSampler].AddExposedLog(log)
				log.IsExposed = true
			}

			// 添加日志到缓存
			// TODO 加入已导出日志到缓存仅仅用于检测无法匹配到任何日志的信号
			// 仅影响日志输出,为了性能优化跳过已导出的日志
			if !log.IsExposed {
				g.AddIntoBuffer(log)
			}
		}
	}
}

// GetExposedLogs 从缓存的全部日志中搜索采样的日志
// TODO 优化: 目前所有流水线共用全局日志缓存; 每个流水线的数据进行采样时,存在重复遍历;
func (g *GlobalCache) GetExposedLogs(signal *LogSignalStatus) {
	// 锁samplerMap
	g.samplerMux.RLock()
	defer g.samplerMux.RUnlock()

	// 检查因配置更新失效的Sampler
	closedSampler := []int{}
	for id, sampler := range g.samplerMaps {
		if sampler == nil {
			closedSampler = append(closedSampler, id)
			continue
		}
		outLogs := g.GetCachedLogsWithSignal(signal, sampler)
		// 锁samplerExposedLog
		sampler.AddExposedLogs(outLogs)
	}
	// 清理失效的sampler
	for _, id := range closedSampler {
		delete(g.samplerMaps, id)
	}
}

func (g *GlobalCache) GetCachedLogsWithSignal(signal *LogSignalStatus, s *SignalSampler) []*CacheLog {
	var result []*CacheLog = make([]*CacheLog, 0)
	if g.RingCacheBuffer.StartIndex <= g.RingCacheBuffer.EndIndex {
		result = g.searchRange(signal, s.SamplerId, result, g.RingCacheBuffer.StartIndex, g.RingCacheBuffer.EndIndex)
	} else {
		result = g.searchRange(signal, s.SamplerId, result, g.RingCacheBuffer.StartIndex, g.RingCacheBuffer.MaxSize)
		result = g.searchRange(signal, s.SamplerId, result, 0, g.RingCacheBuffer.EndIndex)
	}
	return result
}
