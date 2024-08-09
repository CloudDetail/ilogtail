package pathtopid

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/pkg/logger"
	"github.com/s3rj1k/go-fanotify/fanotify"
	"golang.org/x/sys/unix"
)

const MAX_MARK = 8192

type fanotifyCache struct {
	notify   *fanotify.NotifyFD
	hostDir  string
	maxFiles int

	// path2pid -> host_dir + path
	path2pid map[string]*info
	mu       sync.RWMutex
}

type eventType int

const (
	CLOSE eventType = iota
	MODIFY
)

type notifyEvent struct {
	path   string
	pid    int
	evType eventType
}

func (f *fanotifyCache) Init() {
	notify, err := fanotify.Initialize(
		unix.FAN_CLOEXEC|
			unix.FAN_CLASS_NOTIF,
		os.O_RDONLY|
			unix.O_LARGEFILE|
			unix.O_CLOEXEC,
	)
	if err != nil {
		logger.Errorf(context.Background(), "init notify", "err: %v", err)
	} else {
		f.notify = notify
	}
	if val, ok := os.LookupEnv("HOST_DIR"); ok {
		f.hostDir = val
	} else {
		f.hostDir = ""
	}
	f.maxFiles = 0
	f.path2pid = make(map[string]*info)
}

func (f *fanotifyCache) AddPath(path string) {
	if f.maxFiles >= MAX_MARK {
		logger.Warning(context.Background(), "add path err: maxfiles reached")
		return
	}
	if err := f.notify.Mark(
		unix.FAN_MARK_ADD,
		unix.FAN_MODIFY|
			unix.FAN_CLOSE_WRITE,
		unix.AT_FDCWD,
		path,
	); err != nil {
		logger.Errorf(context.Background(), "add mark notify", "path: %v, err: %v", path, err)
		return
	}
	f.maxFiles++
}

func (f *fanotifyCache) RemovePath(path string) {
	if f.maxFiles < 1 {
		logger.Warning(context.Background(), "remove path err: not watch file")
		return
	}

	if err := f.notify.Mark(
		unix.FAN_MARK_REMOVE,
		0,
		unix.AT_FDCWD,
		path,
	); err != nil {
		logger.Errorf(context.Background(), "remove mark notify", "path: %v, err: %v", path, err)
		return
	}
	f.maxFiles--
}

func (f *fanotifyCache) continueGetEvent(eventCh chan<- *notifyEvent) {
	for {
		ev, err := f.getEvent()
		if err == nil && ev != nil {
			//logger.Infof(context.Background(), "get event", "event: %v", ev)
			eventCh <- ev
		}
		if err != nil {
			logger.Errorf(context.Background(), "get event", "err: %v", err)
		}
	}
}

func (f *fanotifyCache) startWatchLifeCycle() {
	ch := make(chan *notifyEvent, 100)
	go f.continueGetEvent(ch)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case event := <-ch:
			f.handleEvent(event)
		case <-ticker.C:
			f.cleanExpired()
		}
	}
}

func (f *fanotifyCache) handleEvent(event *notifyEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// logger.Infof(context.Background(), "pid %d type %d path %s", event.pid, event.evType, event.path)
	if event.evType != CLOSE {
		_info, ok := f.path2pid[event.path]
		if ok {
			// 更新最近的使用时间
			_info.timestamp = time.Now().UnixNano()
			_info.pid = event.pid
			_info.init = true
		} else {
			_info = &info{
				timestamp: time.Now().UnixNano(),
				pid:       event.pid,
				init:      true,
			}
			f.path2pid[event.path] = _info
		}
	}
}

func (f *fanotifyCache) getEvent() (*notifyEvent, error) {
	var event notifyEvent
	data, err := f.notify.GetEvent(os.Getpid())

	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	if data == nil {
		return nil, nil
	}

	defer data.Close()

	var ev_type eventType

	if data.MatchMask(unix.FAN_CLOSE_WRITE) {
		ev_type = CLOSE
	} else if data.MatchMask(unix.FAN_MODIFY) {
		ev_type = MODIFY
	} else {
		return nil, nil
	}

	path, err := data.GetPath()
	// 从事件中取出的事件的path不包含hostDir
	path = f.hostDir + path
	if ev_type == MODIFY {
		f.notify.Mark(unix.FAN_MARK_ADD|unix.FAN_MARK_IGNORED_MASK|unix.FAN_MARK_IGNORED_SURV_MODIFY, unix.FAN_MODIFY, unix.AT_FDCWD, path)
	} else if ev_type == CLOSE {
		f.notify.Mark(unix.FAN_MARK_REMOVE|unix.FAN_MARK_IGNORED_MASK|unix.FAN_MARK_IGNORED_SURV_MODIFY, unix.FAN_MODIFY, unix.AT_FDCWD, path)
	}

	if err != nil {
		return nil, err
	}
	event.path = path
	event.pid = data.GetPID()
	event.evType = ev_type

	//return fmt.Sprintf("%s,PID: %d: %s(fd: %d)", eventType, data.GetPID(), path, data.Fd), nil
	//logger.Infof(context.Background(), "getEvent: %s,PID: %d: %s(fd: %d)", ev_type, data.GetPID(), path, data.Fd)
	return &event, nil
}

func (f *fanotifyCache) cleanExpired() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.path2pid {
		if time.Now().UnixNano()-v.timestamp > time.Hour.Nanoseconds() {
			f.RemovePath(k)
			delete(f.path2pid, k)
		}
	}
}

func (f *fanotifyCache) getPidFromPath(path string) *info {
	f.mu.RLock()
	defer f.mu.RUnlock()
	info, ok := f.path2pid[path]
	if ok {
		return info
	}
	return nil
}

func (f *fanotifyCache) addPathWatch(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AddPath(path)
	info := &info{
		pid:       0,
		timestamp: time.Now().UnixNano(),
		init:      false,
	}
	f.path2pid[path] = info
}
