package pathtopid

import (
	"os"
	"strconv"
	"sync"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/alibaba/ilogtail/pkg/protocol"
)

const pluginName = "processor_path_to_pid"

var f *fanotifyCache = &fanotifyCache{}
var initFnotifyOnce sync.Once

func init() {
	pipeline.Processors[pluginName] = func() pipeline.Processor {
		return &ProcessorPathToPid{}
	}
}

type info struct {
	pid       int
	timestamp int64
	init      bool
}

type ProcessorPathToPid struct {
	context pipeline.Context

	host_dir string
}

func (p *ProcessorPathToPid) Description() string {
	return "Get pid from log file path"
}

func (p *ProcessorPathToPid) Init(context pipeline.Context) error {
	initFnotifyOnce.Do(func() {
		f.Init()
	})
	p.context = context
	if val, ok := os.LookupEnv("HOST_DIR"); ok {
		p.host_dir = val
	} else {
		p.host_dir = ""
	}
	go f.startWatchLifeCycle()
	logger.Infof(p.context.GetRuntimeContext(), "Init processor_path_to_pid")
	return nil
}

func (p *ProcessorPathToPid) ProcessLogs(logArray []*protocol.Log) []*protocol.Log {
	for _, log := range logArray {
		p.processLog(log)
	}
	return logArray
}

func (p *ProcessorPathToPid) processLog(log *protocol.Log) {
	for _, content := range log.Contents {
		if content.Key == "__tag__:__path__" {
			info := f.getPidFromPath(content.Value)
			if info == nil {
				f.addPathWatch(content.Value)
			} else if info.init {
				pid_kv := &protocol.Log_Content{Key: "pid", Value: strconv.Itoa(info.pid)}
				log.Contents = append(log.Contents, pid_kv)
			}
		}
	}
}
