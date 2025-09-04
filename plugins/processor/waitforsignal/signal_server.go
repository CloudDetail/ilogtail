package signalsampler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alibaba/ilogtail/pkg/logger"
)

const RegisterSock = "/opt/signal/uds-register.sock"
const RegisterUrl = "/register"
const HealthCheckUrl = "/already-register-healthcheck"

type LogSignalServer struct {
	ServeMux   *http.ServeMux
	SignalChan chan Signal

	RetryRegisterTimes int
	client             *http.Client
}

const ExposeLogURL = "/exposelog"
const ComponentName = "log"

func NewUnixSocketServer(sockAddr string) (*LogSignalServer, error) {
	dir, _ := filepath.Split(sockAddr)
	if sock, err := os.Stat(sockAddr); err == nil {
		if !sock.IsDir() {
			err := os.Remove(sockAddr)
			if err != nil {
				return nil, fmt.Errorf("file[%s] exist , remove failed, err: %v", sockAddr, err)
			}
		} else {
			return nil, fmt.Errorf("filePath file[%s] cannot create, exist dir", sockAddr)
		}
	} else if _, err := os.Stat(dir); os.IsNotExist(err) {
		err := os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			return nil, fmt.Errorf("parentPath dir[%s] cannot create, err: %v", dir, err)
		}
	}

	server := &LogSignalServer{
		ServeMux:   &http.ServeMux{},
		SignalChan: make(chan Signal, 1000),
	}

	server.ServeMux.HandleFunc(ExposeLogURL, server.ExposeLog)
	unixAddr, err := net.ResolveUnixAddr("unix", sockAddr)
	if err != nil {
		return nil, err
	}

	listener, err := net.ListenUnix("unix", unixAddr)
	if err != nil {
		return nil, err
	}

	go http.Serve(listener, server.ServeMux)

	// 向主探针注册自己的地址
	go server.ComponentHealthCheck(RegisterArg{
		Component: ComponentName,
		SockAddr:  sockAddr,
		UrlPath:   ExposeLogURL,
	}, RegisterSock, RegisterUrl, HealthCheckUrl)

	return server, nil
}

type Result struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
}

var OKResult = Result{
	Code:    200,
	Message: "",
}

type RegisterArg struct {
	Component string `json:"component"`
	SockAddr  string `json:"sock_addr,omitempty"`
	UrlPath   string `json:"url_path,omitempty"`
}

func (s *LogSignalServer) ComponentHealthCheck(args RegisterArg, registerSock, registerUrl, healthUrl string) {
	if s.client == nil {
		s.client = &http.Client{
			Transport: &http.Transport{
				Dial: func(network, addr string) (net.Conn, error) {
					return net.Dial("unix", registerSock)
				},
			},
			Timeout: time.Second * 10,
		}
	}

	for {
		time.Sleep(time.Minute)
		isHealth, err := s.healthCheckRequest(args, registerSock, healthUrl)
		if err != nil {
			continue
		}

		if !isHealth {
			s.RetryRegisterTimes++
			s.registerToMainAgent(args, registerSock, registerUrl)
		}
	}
}

func (s *LogSignalServer) healthCheckRequest(args RegisterArg, registerSock string, healthUrl string) (isHealth bool, err error) {
	if _, err := os.Stat(registerSock); err != nil {
		logger.Warning(context.Background(), "REGISTER_FAILED", "invalid registerSock", err)
		return false, err
	}
	body, err := json.Marshal(args)
	if err != nil {
		logger.Warning(context.Background(), "REGISTER_FAILED", "invalid registerArgs", err)
	}
	resp, err := s.client.Post(fmt.Sprintf("http://localhost%s", healthUrl), "application/json", bytes.NewReader(body))
	if err != nil {
		logger.Warning(context.Background(), "REGISTER_FAILED", "health check failed,network err", err)
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, nil
	}

	respBody := make([]byte, resp.ContentLength)
	resp.Body.Read(respBody)

	if string(respBody) != "ok" {
		return false, nil
	}
	return true, nil
}

func (s *LogSignalServer) registerToMainAgent(args RegisterArg, registerSock string, registerUrl string) {
	// Wait For MainAgent Ready
	tries := 0
	for {
		if _, err := os.Stat(registerSock); err == nil {
			break
		}
		tries++
		if tries > 10 {
			logger.Warning(context.Background(), "REGISTER_FAILED", "register sock not found", registerSock)
			return
		}
		time.Sleep(time.Second * 30)
	}
	s.RetryRegisterTimes++
	body, err := json.Marshal(args)
	if err != nil {
		logger.Warning(context.Background(), "REGISTER_FAILED", "err", err)
	}

	resp, err := s.client.Post(fmt.Sprintf("http://localhost%s", registerUrl), "application/json", bytes.NewReader(body))
	if err != nil {
		logger.Warning(context.Background(), "REGISTER_FAILED", "err", err)
		return
	}
	defer resp.Body.Close()
	logger.Info(context.Background(), "register to main agent success, response", resp.StatusCode)
}
