package logtoprommetric

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/alibaba/ilogtail/pkg/protocol"
	parser "github.com/coroot/logparser"
	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

var HOSTNAME string

func init() {
	var find bool
	HOSTNAME, find = os.LookupEnv("_node_name_")
	if !find {
		HOSTNAME, _ = os.Hostname()
	}
}

const pluginName = "processor_log_to_prom_metric"

var promSDK *OTLPSDK
var sdkInitOnce sync.Once

// 注册插件
func init() {
	pipeline.Processors[pluginName] = func() pipeline.Processor {
		return &ProcessorLogToPromMetric{}
	}
}

type ProcessorLogToPromMetric struct {
	PromPort string

	context pipeline.Context
	// lastLogInfo map[ServiceTag]*LastLogInfo
	lastLogInfo sync.Map
}

func (p *ProcessorLogToPromMetric) ProcessLogs(logArray []*protocol.Log) []*protocol.Log {
TraverseLogArray:
	for _, log := range logArray {
		contents := log.GetContents()
		if contents == nil {
			continue TraverseLogArray
		}

		var svcTag = &ServiceTag{
			Node: HOSTNAME,
		}
		var logContentIdx = -1
		for i, cont := range log.Contents {
			if log.Contents[i] == nil {
				continue
			}

			if cont.Key != "content" {
				svcTag.FillTag(cont.Key, cont.Value)
				continue
			}

			logContentIdx = i
			// Ignore non-first line logs
			if !parser.IsFirstLine(cont.Value) {
				continue TraverseLogArray
			}
		}

		if logContentIdx == -1 {
			continue TraverseLogArray
		}
		lastLogInfo := p.GetLastLogInfo(svcTag)
		logContent := log.Contents[logContentIdx].Value

		if lastLogInfo == nil {
			lastLogInfo = &LastLogInfo{
				isLastNewLine:                true,
				isFirstLineContainsTimestamp: parser.IsContainsTimestamp(logContent),
				pythonTraceback:              false,
				pythonTracebackExpected:      false,
				TimestampSecond:              int64(log.Time),
			}
			p.UpdateLogInfo(svcTag, lastLogInfo)
		}

		lastLogInfo.TimestampSecond = int64(log.Time)

		if !lastLogInfo.IsFirstLine(logContent) {
			lastLogInfo.isLastNewLine = false
			continue TraverseLogArray
		}
		lastLogInfo.isLastNewLine = true

		logLevel, exceptionType := parser.GuessLevelAndException(logContent)
		p.CounterInc(svcTag, logLevel, exceptionType)
	}
	return logArray
}

func (p *ProcessorLogToPromMetric) CounterInc(serviceTag *ServiceTag, logLevel parser.Level, exceptionType string) {
	promSDK.levelCounter.Add(context.Background(), 1, serviceTag.WithLogLevel(logLevel))
	if len(exceptionType) > 0 {
		promSDK.exceptionCounter.Add(context.Background(), 1, serviceTag.WithExceptionType(exceptionType))
	}
}

// Description implements pipeline.Processor.
func (p *ProcessorLogToPromMetric) Description() string {
	return "Parse fields from logs into Prometheus metrics."
}

// Init implements pipeline.Processor.
func (p *ProcessorLogToPromMetric) Init(context pipeline.Context) error {
	p.context = context
	sdkInitOnce.Do(func() {
		promSDK = InitMeter(context, p.PromPort)
	})
	go p.CleanupExpiredInfo()
	return nil
}

func (p *ProcessorLogToPromMetric) GetLastLogInfo(serviceTag *ServiceTag) *LastLogInfo {
	ref, find := p.lastLogInfo.Load(*serviceTag)
	if !find {
		return nil
	}
	return ref.(*LastLogInfo)
}

func (p *ProcessorLogToPromMetric) UpdateLogInfo(serviceTag *ServiceTag, info *LastLogInfo) {
	p.lastLogInfo.Store(*serviceTag, info)
}

func (p *ProcessorLogToPromMetric) CleanupExpiredInfo() {
	ticker := time.NewTicker(5 * time.Minute)
	for {
		select {
		case <-ticker.C:
			now := time.Now().Unix()
			p.lastLogInfo.Range(func(key, value interface{}) bool {
				serviceTag := key.(ServiceTag)
				lastLogInfo := value.(*LastLogInfo)
				if lastLogInfo.TimestampSecond+30 < now {
					p.lastLogInfo.Delete(serviceTag)
				}
				return true
			})
		case <-p.context.GetRuntimeContext().Done():
			return
		}
	}
}

type ServiceTag struct {
	// ServiceName string
	PodName       string
	Namespace     string
	ContainerName string
	ContainerId   string
	SourceFrom    string
	Pid           string
	Node          string
}

func (s *ServiceTag) FillTag(key string, value string) {
	switch key {
	case "_pod_name_":
		s.PodName = value
	case "_namespace_":
		s.Namespace = value
	case "_container_name_":
		s.ContainerName = value
	case "_source_", "__tag__:__path__":
		s.SourceFrom = value
	case "_container_id_":
		if len(value) > 12 {
			value = value[0:12]
		}
		s.ContainerId = value
	case "pid":
		s.Pid = value
	}
}

func (s *ServiceTag) WithLogLevel(level parser.Level) api.MeasurementOption {
	if len(s.ContainerId) > 0 {
		// 容器环境使用containerID
		return api.WithAttributeSet(attribute.NewSet(
			attribute.Key("container_name").String(s.ContainerName),
			attribute.Key("container_id").String(s.ContainerId),
			attribute.Key("level").String(level.String()),
			attribute.Key("namespace").String(s.Namespace),
			attribute.Key("pod_name").String(s.PodName),
			attribute.Key("source_from").String(s.SourceFrom),
			attribute.Key("node").String(s.Node),
		))
	} else {
		// 非容器环境使用pid
		return api.WithAttributeSet(attribute.NewSet(
			attribute.Key("level").String(level.String()),
			attribute.Key("pid").String(s.Pid),
			attribute.Key("source_from").String(s.SourceFrom),
			attribute.Key("node").String(s.Node),
		))
	}

}

func (s *ServiceTag) WithExceptionType(exceptionType string) api.MeasurementOption {
	if len(s.ContainerId) > 0 {
		return api.WithAttributeSet(attribute.NewSet(
			attribute.Key("container_name").String(s.ContainerName),
			attribute.Key("container_id").String(s.ContainerId),
			attribute.Key("exception").String(exceptionType),
			attribute.Key("namespace").String(s.Namespace),
			attribute.Key("pod_name").String(s.PodName),
			attribute.Key("source_from").String(s.SourceFrom),
		))
	} else {
		return api.WithAttributeSet(attribute.NewSet(
			attribute.Key("pid").String(s.Pid),
			attribute.Key("exception").String(exceptionType),
			attribute.Key("source_from").String(s.SourceFrom),
		))
	}
}

type LastLogInfo struct {
	isLastNewLine                bool
	isFirstLineContainsTimestamp bool

	pythonTraceback         bool
	pythonTracebackExpected bool

	// 上次日志的时间戳
	TimestampSecond int64
}

func (l *LastLogInfo) IsFirstLine(content string) bool {
	if l.isFirstLineContainsTimestamp {
		return parser.IsContainsTimestamp(content)
	}

	if strings.HasPrefix(content, "Traceback ") {
		l.pythonTraceback = true
		if l.pythonTracebackExpected {
			l.pythonTracebackExpected = false
			return false
		}
		return !l.isLastNewLine
	}
	if content == "The above exception was the direct cause of the following exception:" ||
		content == "During handling of the above exception, another exception occurred:" {
		l.pythonTracebackExpected = true
		return false
	}
	if l.pythonTraceback {
		l.pythonTraceback = false
		return false
	}

	return true
}
