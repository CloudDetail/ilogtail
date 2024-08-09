package logtoprommetric

import (
	"context"
	"net"
	"net/http"
	"os"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/alibaba/ilogtail/pkg/pipeline"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	api "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.25.0"
)

type OTLPSDK struct {
	context pipeline.Context
	meter   api.Meter

	levelCounter     api.Int64Counter
	exceptionCounter api.Int64Counter

	listenAddress string
	srvMux        *http.ServeMux
}

const meterName = "github.com/alibaba/ilogtail/logmetric"

func InitMeter(context pipeline.Context, address string) *OTLPSDK {
	sdk := &OTLPSDK{
		context:       context,
		srvMux:        http.NewServeMux(),
		listenAddress: address,
	}

	exporter, err := prometheus.New()
	if err != nil {
		logger.Error(
			context.GetRuntimeContext(),
			"PROM_EXPORTER_ERROR",
			"err", err.Error(),
		)
	}

	hostname, find := os.LookupEnv("_node_name_")
	if !find {
		hostname, _ = os.Hostname()
	}
	hostIP := os.Getenv("_node_ip_")
	res, err := resource.New(context.GetRuntimeContext(),
		resource.WithAttributes(
			semconv.HostIPKey.String(hostIP),
			semconv.HostName(hostname),
		),
	)
	if err != nil {
		logger.Error(
			context.GetRuntimeContext(),
			"PROM_EXPORTER_ERROR",
			"err", err.Error(),
		)
	}
	provider := metric.NewMeterProvider(metric.WithReader(exporter), metric.WithResource(res))
	sdk.meter = provider.Meter(meterName)

	// TODO deal error
	sdk.levelCounter, _ = sdk.meter.Int64Counter(
		"originx_logparser_level_count",
		api.WithDescription("log level counter"))
	sdk.exceptionCounter, _ = sdk.meter.Int64Counter(
		"originx_logparser_exception_count",
		api.WithDescription("log exception counter"))

	logger.Info(
		context.GetRuntimeContext(),
		"PROM_EXPORTER_INIT",
		"addr", "serving metrics at"+address)

	sdk.srvMux.Handle("/metrics", promhttp.Handler())
	setUpHTTPServer(sdk.srvMux, address)

	return sdk
}

func setUpHTTPServer(handler http.Handler, listenAddr string) (*http.Server, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	server := &http.Server{Handler: handler}
	go func() {
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			logger.Error(
				context.Background(),
				"PROM_EXPORTER_ERROR",
				"prometheus exporter err:", err.Error())
		}
		listener.Close()
	}()
	return server, nil
}
