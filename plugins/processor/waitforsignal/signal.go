package signalsampler

import (
	"github.com/gin-gonic/gin"
)

// Signal 从主探针接收的采集信号
type Signal struct {
	StartTS uint32 `json:"start_ts"`
	EndTS   uint32 `json:"end_ts"`

	// 筛选条件
	ContainerId string `json:"container_id"`

	PidStr string `json:"pid"`

	// Metadata
	ApmSpanId  string `json:"span_id"`
	ApmTraceId string `json:"trace_id"`

	// Namespace     string `json:"namespace"`
	// PodName       string `json:"pod_name"`
	// ContainerName string `json:"container_name"`
}

func (ls *LogSignalServer) ExposeLog(c *gin.Context) {
	signal := Signal{}
	c.BindJSON(&signal)
	ls.SignalChan <- signal
	c.JSON(200, OKResult)
}
