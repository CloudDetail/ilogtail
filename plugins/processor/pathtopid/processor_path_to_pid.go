package pathtopid

import (
	"os"
	"strconv"
	"sync"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/models"
	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/alibaba/ilogtail/pkg/protocol"
)

const pluginName = "processor_path_to_pid"

const v1_path_key = "__tag__:__path__"
const v2_path_key = "__path__"
const pid_key = "pid"
const expected_path_key = "_source_"

var f *fanotifyCache = &fanotifyCache{}
var initFanotifyOnce sync.Once

func init() {
	pipeline.Processors[pluginName] = func() pipeline.Processor {
		return &ProcessorPathToPid{}
	}
}

type info struct {
	pid       int
	timestamp int64
	init      bool
	dev       uint64
	ino       uint64
}

type ProcessorPathToPid struct {
	context pipeline.Context

	ParseMount bool
	host_dir   string
}

func (p *ProcessorPathToPid) Description() string {
	return "Get pid from log file path"
}

func (p *ProcessorPathToPid) Init(context pipeline.Context) error {
	initFanotifyOnce.Do(func() {
		f.Init(context)
	})

	if p.ParseMount && !f.parseMount {
		f.rawPath2MountPath = make(map[rawPKey]string)
		f.parseMount = true
	}

	p.context = context
	if val, ok := os.LookupEnv("HOST_DIR"); ok {
		p.host_dir = val
	} else {
		p.host_dir = ""
	}

	if f != nil && f.notify != nil {
		go f.startWatchLifeCycle()
	} else {
		logger.Errorf(p.context.GetRuntimeContext(), "INIT NOTIFY FAILED", "Init processor_path_to_pid failed, fanotify not init")
	}

	logger.Infof(p.context.GetRuntimeContext(), "Init processor_path_to_pid")
	return nil
}

// Process... log only
func (p *ProcessorPathToPid) Process(in *models.PipelineGroupEvents, context pipeline.PipelineContext) {
	// DEBUG
	for k, v := range in.Group.Tags.Iterator() {
		logger.Info(p.context.GetRuntimeContext(), "GROUP TAG", "k", k, "v", v)
	}

	for _, event := range in.Events {
		logger.Info(p.context.GetRuntimeContext(), "TIMESTAMP", event.GetTimestamp())
	}

	if in.Group != nil && in.Group.Tags != nil {
		path := in.Group.Tags.Get(v2_path_key)
		if len(path) >= 0 {
			info := f.getPidFromPath(path)
			if info == nil {
				f.addPathWatch(path)
			} else if info.init {
				in.Group.Tags.Add(pid_key, strconv.Itoa(info.pid))
			}
			in.Group.Tags.Add(expected_path_key, path)
		}
	}

	context.Collector().Collect(in.Group, in.Events...)
}

func (p *ProcessorPathToPid) ProcessLogs(logArray []*protocol.Log) []*protocol.Log {
	for _, log := range logArray {
		p.processLog(log)
	}
	return logArray
}

func (p *ProcessorPathToPid) processLog(log *protocol.Log) {
	if f == nil || f.notify == nil {
		return
	}
	for _, content := range log.Contents {
		if content.Key != v1_path_key {
			continue
		}

		log.Contents = append(log.Contents, &protocol.Log_Content{Key: expected_path_key, Value: content.Value})
		info := f.getPidFromPath(content.Value)
		if info == nil {
			f.addPathWatch(content.Value)
		} else if info.init {
			log.Contents = append(log.Contents, &protocol.Log_Content{Key: pid_key, Value: strconv.Itoa(info.pid)})
		}
		break
	}
}
